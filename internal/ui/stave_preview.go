package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ben-ranford/lopper/internal/terminal"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/capability"
	"github.com/ben-ranford/stave/event"
	"github.com/ben-ranford/stave/layout"
	"github.com/ben-ranford/stave/render"
	"github.com/ben-ranford/stave/semantic"
	"github.com/ben-ranford/stave/state"
	"github.com/ben-ranford/stave/theme"
	charmterm "github.com/charmbracelet/x/term"
)

const staveTUIFeature = "stave-tui-preview"

const (
	staveActionQuit            = "lopper.summary.quit.v1"
	staveActionRefresh         = "lopper.summary.refresh.v1"
	staveActionOpen            = "lopper.summary.open.v1"
	staveActionApplyCodemod    = "lopper.summary.apply-codemod.v1"
	staveActionSaveBaseline    = "lopper.summary.save-baseline.v1"
	staveActionCompareBaseline = "lopper.summary.compare-baseline.v1"
)

// StavePreview is deliberately a delegating TUI: the default path remains
// byte-for-byte owned by Summary, while explicit opt-in routes both Start and
// Snapshot through Stave without changing Lopper's command grammar or action
// authority.
type StavePreview struct {
	legacy *Summary
}

func NewStavePreview(legacy *Summary) TUI { return &StavePreview{legacy: legacy} }

func (p *StavePreview) Snapshot(ctx context.Context, opts Options, outputPath string) error {
	if !opts.UseStavePreview || !opts.Features.Enabled(staveTUIFeature) {
		return p.legacy.Snapshot(ctx, opts, outputPath)
	}
	if outputPath == "" {
		return fmt.Errorf("snapshot output path is required")
	}
	output, err := p.render(ctx, opts)
	if err != nil {
		return err
	}
	if outputPath == "-" {
		writer := p.legacy.Out
		if writer == nil {
			writer = os.Stdout
		}
		_, err = io.WriteString(writer, output)
		return err
	}
	if err := os.WriteFile(outputPath, []byte(output), 0o600); err != nil {
		return err
	}
	if p.legacy.Out != nil {
		_, err = fmt.Fprintf(p.legacy.Out, "Snapshot written to %s\n", outputPath)
	}
	return err
}

func (p *StavePreview) Start(ctx context.Context, opts Options) error {
	if !opts.UseStavePreview || !opts.Features.Enabled(staveTUIFeature) {
		return p.legacy.Start(ctx, opts)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	opts = p.legacy.applyDefaults(opts)
	view, err := p.legacy.analyseSummaryView(ctx, opts)
	if err != nil {
		return err
	}
	writer := p.legacy.Out
	if writer == nil {
		writer = os.Stdout
	}
	reader := bufio.NewReader(p.legacy.In)
	state := buildSummaryState(opts)
	var lineCallCounter uint64
	tty := supportsScreenRefresh(writer)
	if tty {
		if width, _, ok := staveTerminalDimensions(writer); ok {
			opts.Width = width
		}
	}
	program, err := newLopperStaveProgram(p.legacy, &opts, &view, &state)
	if err != nil {
		return err
	}
	sessionOpts := staveSessionOptions(opts, tty)
	prepared, err := program.NewSession(ctx, sessionOpts)
	if err != nil {
		return err
	}
	defer prepared.Session.Close()
	if supportsStaveFullScreen(sessionOpts.RuntimeDetected) {
		return p.runStaveTerminal(ctx, opts, view, state, prepared, p.legacy.In, writer, sessionOpts.RuntimeDetected.AlternateScreen)
	}
	for {
		if tty {
			if width, height, ok := staveTerminalDimensions(writer); ok && (width != sessionOpts.Viewport.Width || height != sessionOpts.Viewport.Height) {
				resize, resizeErr := event.New(event.Resize, event.ResizePayload{Width: width, Height: height})
				if resizeErr != nil {
					return resizeErr
				}
				if resizeErr = sendLopperEvent(ctx, prepared, resize); resizeErr != nil {
					return resizeErr
				}
				sessionOpts.Viewport = layout.Size{Width: width, Height: height}
			}
		}
		snapshot, err := prepared.Session.Snapshot()
		if err != nil {
			return err
		}
		frame, renderErr := render.Render(render.Request{Context: ctx, Tree: snapshot.Tree, Capabilities: snapshot.Capabilities, Theme: prepared.Theme, Viewport: sessionOpts.Viewport})
		if renderErr != nil {
			return renderErr
		}
		output := frame.Terminal
		if err := writeStaveLineFrame(writer, output); err != nil {
			return err
		}
		input, eof, err := readStaveLineInput(reader)
		if err != nil {
			return err
		}
		if eof && input == "" {
			return nil
		}
		current, snapErr := prepared.Session.Snapshot()
		if snapErr != nil {
			return snapErr
		}
		actionID, args, confirm, handled := lopperStaveInput(input, current.Model.interaction.summary)
		if handled {
			args, err = prepareLopperActionArgs(current.Model, actionID, args)
			if err != nil {
				return err
			}
			reportedFailure := false
			lineCallCounter++
			callID := fmt.Sprintf("lopper-line-%d", lineCallCounter)
			ev, err := event.New(event.ActionInvoked, event.ActionInvokedPayload{CallID: callID, ActionID: string(actionID), Arguments: args})
			if err != nil {
				return err
			}
			if err := sendLopperEvent(ctx, prepared, ev); err != nil {
				return err
			}
			// Line-compatible sessions serialize frames, but action execution
			// still runs off-loop after ActionInvoked has been recorded.
			completed := <-startLopperAction(ctx, prepared, actionID, args, "lopper-preview", confirm, callID)
			result, invokeErr := completed.result, completed.err
			if invokeErr != nil {
				var reported *summaryActionReportedError
				if !errors.As(invokeErr, &reported) {
					done, eventErr := event.New(event.EffectResult, event.EffectResultPayload{CallID: callID, Status: "error", Error: terminal.SanitizeString(invokeErr.Error())})
					if eventErr != nil {
						return eventErr
					}
					if eventErr = sendLopperEvent(ctx, prepared, done); eventErr != nil {
						return eventErr
					}
				}
				reportedFailure = true
			}
			if !reportedFailure {
				value := any(nil)
				if result.Outcome != nil {
					value = result.Outcome.Value
				}
				done, eventErr := event.New(event.EffectResult, event.EffectResultPayload{CallID: callID, Status: "completed", Value: value})
				if eventErr != nil {
					return eventErr
				}
				if eventErr = sendLopperEvent(ctx, prepared, done); eventErr != nil {
					return eventErr
				}
			}
			if actionID == action.ID(staveActionQuit) {
				return nil
			}
			// A final unterminated command is valid input. It is processed once,
			// then EOF terminates the line-compatible session cleanly.
			if eof {
				final, renderErr := renderStaveSessionFrame(ctx, prepared, sessionOpts)
				if renderErr != nil {
					return renderErr
				}
				return writeStaveLineFrame(writer, final)
			}
			continue
		}
		textEvent, err := staveCommandEvent(input, current.Model)
		if err != nil {
			return err
		}
		if err := sendLopperEvent(ctx, prepared, textEvent); err != nil {
			return err
		}
		if eof {
			final, renderErr := renderStaveSessionFrame(ctx, prepared, sessionOpts)
			if renderErr != nil {
				return renderErr
			}
			return writeStaveLineFrame(writer, final)
		}
	}
}

func staveTerminalDimensions(writer io.Writer) (width, height int, ok bool) {
	file, ok := writer.(*os.File)
	if !ok {
		return 0, 0, false
	}
	width, height, err := charmterm.GetSize(file.Fd())
	if err != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

func renderStaveSessionFrame(ctx context.Context, prepared *stave.Prepared[staveSummaryModel], opts stave.SessionOptions) (string, error) {
	snapshot, err := prepared.Session.Snapshot()
	if err != nil {
		return "", err
	}
	frame, err := render.Render(render.Request{Context: ctx, Tree: snapshot.Tree, Capabilities: snapshot.Capabilities, Theme: prepared.Theme, Viewport: opts.Viewport})
	if err != nil {
		return "", err
	}
	return frame.Terminal, nil
}

func writeStaveLineFrame(writer io.Writer, output string) error {
	if _, err := io.WriteString(writer, output); err != nil {
		return err
	}
	if !strings.HasSuffix(output, "\n") {
		_, err := io.WriteString(writer, "\n")
		return err
	}
	return nil
}

// readStaveLineInput preserves the distinction between a complete line and a
// final unterminated command. The latter must be applied once before EOF ends
// a non-TTY preview session; an empty EOF is simply a clean exit.
func readStaveLineInput(reader *bufio.Reader) (string, bool, error) {
	input, err := reader.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			return strings.TrimSpace(input), true, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(input), false, nil
}

func sendLopperEvent(ctx context.Context, prepared *stave.Prepared[staveSummaryModel], ev event.Event) error {
	snapshot, err := prepared.Session.Snapshot()
	if err != nil {
		return err
	}
	if err := prepared.Session.Send(ev); err != nil {
		return err
	}
	return prepared.Session.Wait(ctx, func(next state.State[staveSummaryModel]) bool { return next.Sequence > snapshot.Sequence })
}

func staveSessionOptions(opts Options, tty bool) stave.SessionOptions {
	width := opts.Width
	if width == 0 {
		width = 80
	}
	return stave.SessionOptions{SessionID: "lopper-preview", RuntimeDetected: capability.DetectEnv(map[string]string{"TERM": os.Getenv("TERM"), "COLORTERM": os.Getenv("COLORTERM"), "NO_COLOR": os.Getenv("NO_COLOR")}, tty, width, 24), Viewport: layout.Size{Width: width, Height: 24}}
}

func supportsStaveFullScreen(caps capability.Manifest) bool {
	resolved, _ := (capability.Negotiation{RuntimeDetected: caps}).Resolve()
	return resolved.TTY && resolved.Interactive && !resolved.ColorDisabled && resolved.CursorAddressing && resolved.AlternateScreen
}

func lopperStaveInput(input string, state summaryState) (action.ID, any, bool, bool) {
	trimmed := strings.TrimSpace(input)
	switch trimmed {
	case "q", "quit":
		return action.ID(staveActionQuit), map[string]any{}, false, true
	case "", "refresh":
		return action.ID(staveActionRefresh), map[string]any{}, false, true
	}
	if dep, ok := isDetailCommand(trimmed); ok {
		return action.ID(staveActionOpen), map[string]any{"dependency": dep}, false, true
	}
	parsed, ok, err := parseSummaryAction(trimmed, &state)
	if ok && err == nil {
		switch parsed.kind {
		case summaryActionApplyCodemod:
			return action.ID(staveActionApplyCodemod), map[string]any{"dependency": parsed.dependency, "confirm": parsed.confirm, "allowDirty": parsed.allowDirty}, parsed.confirm, true
		case summaryActionSaveBaseline:
			return action.ID(staveActionSaveBaseline), map[string]any{"label": parsed.baselineLabel, "key": parsed.baselineKey, "store": parsed.baselineStorePath}, false, true
		case summaryActionCompareBaseline:
			return action.ID(staveActionCompareBaseline), map[string]any{"key": parsed.baselineKey, "store": parsed.baselineStorePath, "file": parsed.baselinePath, "target": parsed.baselineTarget}, false, true
		}
	}
	for _, item := range []struct{ command, id string }{{"filter", "lopper.summary.filter.v1"}, {"sort", "lopper.summary.sort.v1"}, {"page", "lopper.summary.page.v1"}, {"size", "lopper.summary.size.v1"}} {
		if strings.HasPrefix(trimmed, item.command+" ") {
			return action.ID(item.id), map[string]any{"value": strings.TrimSpace(strings.TrimPrefix(trimmed, item.command+" "))}, false, true
		}
	}
	return "", nil, false, false
}

func (p *StavePreview) render(ctx context.Context, opts Options) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	opts = p.legacy.applyDefaults(opts)
	view, err := p.legacy.analyseSummaryView(ctx, opts)
	if err != nil {
		return "", err
	}
	state := buildSummaryState(opts)
	return p.renderView(ctx, opts, view, state)
}

func (p *StavePreview) renderView(ctx context.Context, opts Options, view summaryReportView, state summaryState) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	renderer, err := newStaveRenderer(opts, supportsScreenRefresh(p.legacy.Out))
	if err != nil {
		return "", err
	}
	sorted, paged, state, totalPages := runSummaryDependencyPipeline(view, state)
	tree, err := staveTree(view, sorted, paged, state, totalPages, renderer.ASCII)
	if err != nil {
		return "", err
	}
	output, err := render.Render(render.Request{Context: ctx, Tree: tree, Theme: renderer.Theme, Capabilities: renderer.Caps, Viewport: layout.Size{Width: renderer.Caps.Width, Height: maxInt(1, renderer.Caps.Height)}})
	if err != nil {
		return "", fmt.Errorf("render Stave preview: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if output.Terminal == "" {
		return tree.Snapshot().Root.Value().Text + "\n", nil
	}
	return output.Terminal, nil
}

func staveTree(view summaryReportView, sorted, deps []summaryDependencyView, state summaryState, totalPages int, ascii bool) (semantic.Tree, error) {
	return staveTreeForInteraction(view, sorted, deps, state, totalPages, ascii, staveSummaryInteraction{summary: state, help: state.showHelp})
}

func staveTreeForInteraction(view summaryReportView, sorted, deps []summaryDependencyView, state summaryState, totalPages int, ascii bool, interaction staveSummaryInteraction) (semantic.Tree, error) {
	if interaction.focusPane == "" {
		return staveSnapshotTree(view, sorted, deps, state, totalPages, ascii, interaction.help)
	}
	return staveInteractiveTree(view, sorted, deps, state, totalPages, ascii, interaction)
}

func staveSnapshotTree(view summaryReportView, sorted, deps []summaryDependencyView, state summaryState, totalPages int, ascii, showHelp bool) (semantic.Tree, error) {
	separator := staveSeparator(ascii)
	children := make([]semantic.Node, 0, len(deps)+len(view.Warnings)+2)
	baseStatus := fmt.Sprintf("page %d/%d%s%d dependencies%s%d page size%sStave preview", state.page, totalPages, separator, len(sorted), separator, state.pageSize, separator)
	status := baseStatus
	if state.filter == "" {
		status += " filter none" + separator + "focus summary"
	} else {
		status += separator + "filter " + safeDisplay(state.filter, ascii)
	}
	statusNode, err := staveRecordNode("status", "summary", "status", "heading", "Lopper", status, "domain.primary", nil)
	if err != nil {
		return semantic.Tree{}, err
	}
	children = append(children, statusNode)
	for _, dep := range deps {
		row, err := staveDependencyNode(dep, ascii, false)
		if err != nil {
			return semantic.Tree{}, err
		}
		children = append(children, row)
	}
	for i, warning := range view.Warnings {
		warningNode, err := staveRecordNode("warning", fmt.Sprintf("%d", i), "main", "alert", "Warning", safeDisplay(warning, ascii), "status.advisory", nil)
		if err != nil {
			return semantic.Tree{}, err
		}
		children = append(children, warningNode)
	}
	if showHelp {
		helpNode, err := staveRecordNode("help", "summary", "footer", "status", "Help", "Commands: / filter | : command | arrows navigate | Enter open | r refresh | q quit", "status.advisory", nil)
		if err != nil {
			return semantic.Tree{}, err
		}
		children = append(children, helpNode)
	}
	return staveApplicationTree(children, baseStatus)
}

func staveInteractiveTree(view summaryReportView, sorted, deps []summaryDependencyView, state summaryState, totalPages int, ascii bool, interaction staveSummaryInteraction) (semantic.Tree, error) {
	width, height := interaction.viewport.Width, interaction.viewport.Height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	separator := staveSeparator(ascii)
	selected := clampStaveRow(interaction.selectedRow, len(deps))
	detailDep, hasDetail := staveSelectedDetail(view, state.selectedDependency)
	if !hasDetail && interaction.focusPane == "detail" {
		interaction.focusPane = "summary"
	}

	header := fmt.Sprintf("Stave preview%spage %d/%d%s%d deps%s%d/page", separator, state.page, totalPages, separator, len(sorted), separator, state.pageSize)
	if state.filter != "" {
		header += separator + "filter " + safeDisplay(state.filter, ascii)
	}
	if len(view.Warnings) > 0 {
		header += fmt.Sprintf("%s%d warnings", separator, len(view.Warnings))
	}
	headerNode, err := staveRecordNode("status", "summary", "status", "heading", "Status", header, "domain.primary", nil)
	if err != nil {
		return semantic.Tree{}, err
	}
	children := []semantic.Node{headerNode}

	if interaction.help {
		if staveHasFeedback(interaction) {
			feedback, err := staveFeedbackNode(interaction, width, ascii)
			if err != nil {
				return semantic.Tree{}, err
			}
			// On the smallest supported viewport, feedback is the status row.
			// Replacing the summary header keeps the complete help map visible.
			children = []semantic.Node{feedback}
		}
		helpNodes, err := staveHelpNodes(width, ascii)
		if err != nil {
			return semantic.Tree{}, err
		}
		children = append(children, helpNodes...)
		if len(children) > height {
			children = children[:height]
		}
		return staveApplicationTree(children, "Stave preview")
	}

	feedback, err := staveFeedbackNode(interaction, width, ascii)
	if err != nil {
		return semantic.Tree{}, err
	}
	children = append(children, feedback)

	detailNodes := []semantic.Node(nil)
	if hasDetail {
		detailNodes, err = staveDetailNodes(detailDep, interaction.focusPane == "detail", ascii)
		if err != nil {
			return semantic.Tree{}, err
		}
	}
	warningLines := 0
	if len(view.Warnings) > 0 {
		warningLines = 1
	}
	rowBudget := height - len(children) - len(detailNodes) - warningLines
	if len(deps) > 0 && rowBudget < 1 {
		rowBudget = 1
	}
	start, end := staveVisibleRows(len(deps), selected, rowBudget)
	for i := start; i < end; i++ {
		row, err := staveDependencyNode(deps[i], ascii, interaction.focusPane == "summary" && i == selected)
		if err != nil {
			return semantic.Tree{}, err
		}
		children = append(children, row)
	}
	children = append(children, detailNodes...)
	if len(view.Warnings) > 0 {
		warningName := "Warnings"
		if width < 32 {
			warningName = "Warn"
		}
		warningText := fmt.Sprintf("%d%s%s", len(view.Warnings), staveSeparator(ascii), safeDisplay(view.Warnings[0], ascii))
		warningNode, err := staveRecordNode("warning-summary", "summary", "main", "alert", warningName, warningText, "status.advisory", nil)
		if err != nil {
			return semantic.Tree{}, err
		}
		children = append(children, warningNode)
	}
	if len(children) > height {
		children = children[:height]
	}
	return staveApplicationTree(children, "Stave preview")
}

func staveDependencyNode(dep summaryDependencyView, ascii, selected bool) (semantic.Node, error) {
	content := fmt.Sprintf("%s%s%g%% used%s%d bytes waste", safeDisplay(dep.Language, ascii), staveSeparator(ascii), dep.UsedPercent, staveSeparator(ascii), dep.EstimatedUnusedBytes)
	name, style := safeDisplay(dep.Name, ascii), "status.success"
	if selected {
		name, style = "> "+name, "domain.primary"
	}
	// Keys are logical identity, not display text. Sanitizing or ASCII-folding
	// them would make distinct dependencies collide before rendering.
	return staveRecordNode("dependency", dep.Language+"/"+dep.Name, "main", "row", name, content, style, []semantic.ActionRef{{ID: staveActionOpen, Label: "Open dependency", Default: true}, {ID: staveActionApplyCodemod, Label: "Apply codemod"}})
}

func staveDetailNodes(dep summaryDependencyView, focused, ascii bool) ([]semantic.Node, error) {
	name := "Detail"
	style := "status.advisory"
	if focused {
		name = "> Detail"
		style = "domain.primary"
	}
	identity := safeDisplay(dep.Language+":"+dep.Name, ascii)
	lines := []struct {
		kind, name, value, style string
	}{
		{"detail", name, identity, style},
		{"detail-exports", "Exports", fmt.Sprintf("%d/%d used (%g%%)", dep.UsedExportsCount, dep.TotalExportsCount, dep.UsedPercent), "status.unknown"},
		{"detail-waste", "Waste", fmt.Sprintf("%d bytes estimated unused", dep.EstimatedUnusedBytes), "status.unknown"},
	}
	removalValue, removalStyle := "not a candidate", "status.unknown"
	if dep.RemovalCandidate != nil {
		removalValue, removalStyle = fmt.Sprintf("candidate score %g", dep.RemovalCandidate.Score), "status.advisory"
	}
	lines = append(lines, struct {
		kind, name, value, style string
	}{"detail-removal", "Removal", removalValue, removalStyle})
	nodes := make([]semantic.Node, 0, len(lines))
	for _, line := range lines {
		node, err := staveRecordNode(line.kind, dep.Language+"/"+dep.Name, "detail", "status", line.name, line.value, line.style, nil)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func staveFeedbackNode(interaction staveSummaryInteraction, width int, ascii bool) (semantic.Node, error) {
	name, text, style := "Keys", staveKeyHint(width), "status.unknown"
	switch {
	case interaction.error != "":
		name, text, style = "Error", safeDisplay(interaction.error, ascii), "status.failure"
	case interaction.pendingConfirm != "":
		name, text, style = "Confirm", safeDisplay(interaction.pendingConfirm, ascii), "status.advisory"
	case interaction.commandMode:
		name, text, style = "Command", safeDisplay(interaction.filterBuffer, ascii), "domain.primary"
	case interaction.status != "":
		name, text, style = "Update", safeDisplay(interaction.status, ascii), "status.success"
	}
	return staveRecordNode("feedback", "summary", "footer", "status", name, text, style, nil)
}

func staveHelpNodes(width int, ascii bool) ([]semantic.Node, error) {
	type helpLine struct{ name, text string }
	var lines []helpLine
	switch {
	case width < 32:
		lines = []helpLine{{"Nav", "j/k p/n"}, {"Open", "Enter Tab"}, {"Find", "/ filter"}, {"Order", ":sort :size"}, {"Base", ":save :compare"}, {"Code", ":apply confirm"}, {"Exit", "r refresh q"}}
	case width < 72:
		lines = []helpLine{{"Move", "j/k select | p/n page"}, {"Open", "Enter | Tab pane"}, {"Find", "/ filter | : command"}, {"Order", ": sort name|waste | : size N"}, {"Save", ": save-baseline"}, {"Compare", ": compare-baseline"}, {"Apply", ": apply-codemod DEP --confirm"}, {"Exit", "r refresh | q quit"}}
	default:
		lines = []helpLine{{"Navigate", "arrows/j/k select | left/right or p/n page | Enter open | Tab pane"}, {"Filter", "/ text | : filter TEXT | : sort name|waste | : size N"}, {"Baseline", ": save-baseline [options] | : compare-baseline [options]"}, {"Codemod", ": apply-codemod DEP --confirm [--allow-dirty]"}, {"Session", "r refresh | ? close help | q/Esc/Ctrl-C quit"}}
	}
	nodes := make([]semantic.Node, 0, len(lines))
	for i, line := range lines {
		node, err := staveRecordNode("help", fmt.Sprintf("%d", i), "footer", "status", line.name, safeDisplay(line.text, ascii), "status.advisory", nil)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func staveRecordNode(kind, entity, slot, role, name, text, style string, actions []semantic.ActionRef) (semantic.Node, error) {
	return semantic.NewNode(semantic.NodeSpec{Key: &semantic.NodeKey{AppNamespace: "lopper", View: "summary", Kind: kind, Entity: entity, Slot: slot}, Generation: 1, Role: semantic.Role(role), Name: name, Description: text, Value: semantic.Value{Text: text, HasValue: true}, Style: semantic.StyleIntent{Role: style}, Flags: semantic.Flags{Visible: true}, Actions: actions})
}

func staveApplicationTree(children []semantic.Node, description string) (semantic.Tree, error) {
	root, err := semantic.NewNode(semantic.NodeSpec{Key: &semantic.NodeKey{AppNamespace: "lopper", View: "summary", Kind: "application", Entity: "summary", Slot: "main"}, Generation: 1, Role: "application", Name: "Lopper", Description: description, Style: semantic.StyleIntent{Role: "domain.primary"}, Metadata: map[string]string{"layout.kind": "records"}, Flags: semantic.Flags{Visible: true}, Children: children, Actions: []semantic.ActionRef{{ID: staveActionQuit, Label: "Quit"}, {ID: staveActionRefresh, Label: "Refresh", Default: true}, {ID: staveActionSaveBaseline, Label: "Save baseline"}, {ID: staveActionCompareBaseline, Label: "Compare baseline"}}})
	if err != nil {
		return semantic.Tree{}, err
	}
	return semantic.NewTree(1, root)
}

func staveSeparator(ascii bool) string {
	if ascii {
		return " | "
	}
	return " • "
}

func staveKeyHint(width int) string {
	switch {
	case width < 32:
		return "? / : q"
	case width < 64:
		return "? help | / filter | : cmd | q"
	default:
		return "? help | / filter | : commands | arrows navigate | q quit"
	}
}

func staveHasFeedback(interaction staveSummaryInteraction) bool {
	return interaction.error != "" || interaction.pendingConfirm != "" || interaction.commandMode || interaction.status != ""
}

func staveSelectedDetail(view summaryReportView, identity string) (summaryDependencyView, bool) {
	for _, dep := range view.Dependencies {
		if dep.Language+":"+dep.Name == identity {
			return dep, true
		}
	}
	return summaryDependencyView{}, false
}

func clampStaveRow(selected, total int) int {
	if total <= 0 || selected < 0 {
		return 0
	}
	if selected >= total {
		return total - 1
	}
	return selected
}

func staveVisibleRows(total, selected, budget int) (int, int) {
	if total <= 0 || budget <= 0 {
		return 0, 0
	}
	if budget >= total {
		return 0, total
	}
	start := selected - budget/2
	if start < 0 {
		start = 0
	}
	if start+budget > total {
		start = total - budget
	}
	return start, start + budget
}

func safeDisplay(value string, ascii bool) string {
	value = terminal.SanitizeString(value)
	if !ascii {
		return value
	}
	var b strings.Builder
	for _, r := range value {
		if r > 127 {
			b.WriteRune('?')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

type staveRenderer struct {
	Caps  capability.Manifest
	Theme theme.Resolved
	ASCII bool
}

func newStaveRenderer(opts Options, tty bool) (staveRenderer, error) {
	width := opts.Width
	if width == 0 {
		width = 80
		if raw := strings.TrimSpace(os.Getenv("LOPPER_TUI_WIDTH")); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
				width = parsed
			}
		}
	}
	color := true
	if opts.Color != nil {
		color = *opts.Color
	} else if os.Getenv("NO_COLOR") != "" || strings.EqualFold(os.Getenv("TERM"), "dumb") {
		color = false
	}
	env := map[string]string{
		"TERM":      os.Getenv("TERM"),
		"COLORTERM": os.Getenv("COLORTERM"),
		"NO_COLOR":  os.Getenv("NO_COLOR"),
		"CI":        os.Getenv("CI"),
	}
	caps := capability.DetectEnv(env, tty, width, 24)
	if !color {
		caps.Color = capability.ColorNone
		caps.ColorDisabled = true
	}
	ascii := opts.ASCII || width < 40 || caps.Unicode != capability.UnicodeFull
	if ascii {
		caps.Unicode = capability.UnicodeASCII
	}
	t := lopperTheme()
	resolved, err := t.Resolve(theme.ModeDark, theme.DensityComfortable, caps)
	if err != nil {
		return staveRenderer{}, err
	}
	return staveRenderer{Caps: caps, Theme: resolved, ASCII: ascii}, nil
}

func lopperTheme() theme.Theme {
	tokens := theme.TokenSet{}
	for _, role := range theme.RequiredRoleIDs() {
		name := string(role)
		value := "#f2f6f8"
		if strings.Contains(name, "surface") {
			value = "#101418"
		}
		if strings.Contains(name, "border") || strings.Contains(name, "focus") {
			value = "#aebbc5"
		}
		if strings.Contains(name, "link") || strings.Contains(name, "chart") {
			value = "#88b5ff"
		}
		if strings.HasSuffix(name, ".bg") {
			value = "#24313a"
		}
		if name == "domain.primary.bg" || strings.Contains(name, "action.primary.bg") {
			value = "#35d08f"
		}
		if name == "domain.primary.fg" {
			value = "#000000"
		}
		if strings.HasSuffix(name, ".fg") && (strings.Contains(name, "action.") || strings.Contains(name, "status.")) {
			value = "#000000"
		}
		if strings.Contains(name, "action.secondary.bg") || strings.Contains(name, "status.unknown.bg") {
			value = "#d7dce2"
		}
		if strings.Contains(name, "action.destructive.bg") || strings.Contains(name, "status.failure.bg") {
			value = "#f14c4c"
		}
		if strings.Contains(name, "status.success.bg") {
			value = "#35d08f"
		}
		if strings.Contains(name, "status.advisory.bg") {
			value = "#f2c94c"
		}
		if strings.HasPrefix(name, "motion.duration") {
			tokens[role] = theme.Value{Kind: theme.KindDuration, Literal: "120ms"}
			continue
		}
		if strings.HasPrefix(name, "motion.easing") || strings.HasPrefix(name, "type.") {
			tokens[role] = theme.Value{Kind: theme.KindString, Literal: "terminal"}
			continue
		}
		if strings.HasPrefix(name, "space.") || strings.HasPrefix(name, "radius.") || strings.HasPrefix(name, "elevation.") {
			tokens[role] = theme.Value{Kind: theme.KindNumber, Literal: 2}
			continue
		}
		tokens[role] = theme.Value{Kind: theme.KindColor, Literal: value}
	}
	return theme.Theme{ID: "lopper-sap-ember-blight-loam", Version: "v1", Modes: map[theme.Mode]theme.TokenSet{theme.ModeAuto: tokens, theme.ModeDark: {}}, Densities: map[theme.Density]theme.TokenSet{theme.DensityComfortable: {}}, Glyphs: map[string]theme.GlyphSet{"render": {"truncation": {Unicode: "…", ASCII: "...", Width: 3}}}, Assets: map[string]theme.AssetRef{"brand.mark": {ID: "lopper.mark", Text: "L"}, "brand.mark.ascii": {ID: "lopper.mark.ascii", Text: "L"}, "brand.banner.terminal": {ID: "lopper.banner", Text: "SAP / EMBER / BLIGHT / LOAM"}}}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
