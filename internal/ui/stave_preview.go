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
	"time"

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
	staveStylePrimary  = "domain.primary"
	staveStyleAdvisory = "status.advisory"
	staveStyleUnknown  = "status.unknown"
)

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
	state := buildSummaryState(opts)
	interactiveTTY := supportsStaveInteractiveTerminal(p.legacy.In, writer)
	outputTTY := staveTerminalFile(writer)
	if outputTTY {
		if width, _, ok := staveTerminalDimensions(writer); ok {
			opts.Width = width
		}
	}
	program, err := newLopperStaveProgram(p.legacy, &opts, &view, &state)
	if err != nil {
		return err
	}
	sessionOpts := staveSessionOptions(opts, interactiveTTY)
	prepared, err := program.NewSession(ctx, sessionOpts)
	if err != nil {
		return err
	}
	defer prepared.Session.Close()
	if supportsStaveFullScreen(sessionOpts.RuntimeDetected) {
		return p.runStaveTerminal(ctx, opts, prepared, p.legacy.In, writer, sessionOpts.RuntimeDetected.AlternateScreen)
	}
	input := newStaveLineInput(p.legacy.In)
	line := staveLineSession{prepared: prepared, opts: sessionOpts, reader: input.reader, cancelRead: input.cancel, writer: writer, tty: outputTTY}
	return errors.Join(line.run(ctx), input.cleanup())
}

type staveLineSession struct {
	prepared    *stave.Prepared[staveSummaryModel]
	opts        stave.SessionOptions
	reader      *bufio.Reader
	cancelRead  func() bool
	writer      io.Writer
	tty         bool
	callCounter uint64
}

func (s *staveLineSession) run(ctx context.Context) error {
	for {
		if err := s.refreshFrame(ctx); err != nil {
			return err
		}
		input, eof, err := readStaveLineInputContext(ctx, s.reader, s.cancelRead)
		if err != nil {
			return err
		}
		if eof && input == "" {
			return nil
		}
		quit, err := s.command(ctx, input)
		if err != nil {
			return err
		}
		if quit {
			return nil
		}
		// Apply a final unterminated command once, then render its result.
		if eof {
			return s.writeFrame(ctx)
		}
	}
}

func (s *staveLineSession) refreshFrame(ctx context.Context) error {
	if err := s.resize(ctx); err != nil {
		return err
	}
	return s.writeFrame(ctx)
}

func (s *staveLineSession) resize(ctx context.Context) error {
	if !s.tty {
		return nil
	}
	width, height, ok := staveTerminalDimensions(s.writer)
	if !ok || (width == s.opts.Viewport.Width && height == s.opts.Viewport.Height) {
		return nil
	}
	resize, err := event.New(event.Resize, event.ResizePayload{Width: width, Height: height})
	if err != nil {
		return err
	}
	if err := sendLopperEvent(ctx, s.prepared, resize); err != nil {
		return err
	}
	s.opts.Viewport = layout.Size{Width: width, Height: height}
	return nil
}

func (s *staveLineSession) writeFrame(ctx context.Context) error {
	output, err := renderStaveSessionFrame(ctx, s.prepared, s.opts)
	if err != nil {
		return err
	}
	return writeStaveLineFrame(s.writer, output)
}

func (s *staveLineSession) command(ctx context.Context, input string) (bool, error) {
	current, err := s.prepared.Session.Snapshot()
	if err != nil {
		return false, err
	}
	id, args, confirm, handled := lopperStaveInput(input, current.Model.interaction.summary)
	if !handled {
		ev, err := staveCommandEvent(input, current.Model)
		if err != nil {
			return false, err
		}
		return false, sendLopperEvent(ctx, s.prepared, ev)
	}
	args, err = prepareLopperActionArgs(current.Model, id, args)
	if err != nil {
		return false, err
	}
	s.callCounter++
	callID := fmt.Sprintf("lopper-s-%d", s.callCounter)
	ev, err := event.New(event.ActionInvoked, event.ActionInvokedPayload{CallID: callID, ActionID: string(id), Arguments: args})
	if err != nil {
		return false, err
	}
	if err := sendLopperEvent(ctx, s.prepared, ev); err != nil {
		return false, err
	}
	// Serialize frames while executing the action off-loop after its invocation is recorded.
	completed := <-startLopperAction(ctx, s.prepared, id, args, "lopper-preview", confirm, callID)
	if err := completeStaveLineAction(ctx, s.prepared, callID, completed); err != nil {
		return false, err
	}
	return id == action.ID(staveActionQuit), nil
}

func completeStaveLineAction(ctx context.Context, prepared *stave.Prepared[staveSummaryModel], callID string, completed staveActionExecution) error {
	payload := event.EffectResultPayload{CallID: callID, Status: "completed"}
	if completed.err != nil {
		var reported *summaryActionReportedError
		if errors.As(completed.err, &reported) {
			return nil
		}
		payload.Status = "error"
		payload.Error = terminal.SanitizeString(completed.err.Error())
	} else if completed.result.Outcome != nil {
		payload.Value = completed.result.Outcome.Value
	}
	done, err := event.New(event.EffectResult, payload)
	if err != nil {
		return err
	}
	return sendLopperEvent(ctx, prepared, done)
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

// readStaveLineInputContext cancels file-backed reads without consuming input.
// Generic readers have no portable interruption mechanism, so they retain the
// synchronous behavior of readStaveLineInput.
func readStaveLineInputContext(ctx context.Context, reader *bufio.Reader, cancelRead func() bool) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if cancelRead == nil {
		input, eof, err := readStaveLineInput(reader)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", false, ctxErr
		}
		return input, eof, err
	}
	readDone := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			_ = cancelRead()
		case <-readDone:
		}
	}()
	input, eof, err := readStaveLineInput(reader)
	close(readDone)
	<-watchDone
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", false, ctxErr
	}
	return input, eof, err
}

type staveLineInput struct {
	reader  *bufio.Reader
	cancel  func() bool
	cleanup func() error
}

func newStaveLineInput(input io.Reader) staveLineInput {
	if file, ok := input.(*os.File); ok {
		if info, err := file.Stat(); err == nil && info.Mode().IsRegular() {
			return staveLineInput{reader: bufio.NewReader(input), cleanup: func() error { return nil }}
		}
		if err := file.SetReadDeadline(time.Time{}); err == nil {
			return staveLineInput{reader: bufio.NewReader(file), cancel: func() bool { return file.SetReadDeadline(time.Now()) == nil }, cleanup: func() error { return file.SetReadDeadline(time.Time{}) }}
		}
	}
	// A generic io.Reader cannot be interrupted safely. Callers that need an
	// idle read to stop with the context must provide a supported nonregular
	// *os.File. Stave borrows generic readers and never closes them.
	return staveLineInput{reader: bufio.NewReader(input), cleanup: func() error { return nil }}
}

func supportsStaveInteractiveTerminal(input io.Reader, output io.Writer) bool {
	return staveTerminalFile(input) && staveTerminalFile(output)
}

func staveTerminalFile(stream any) bool {
	file, ok := stream.(*os.File)
	return ok && charmterm.IsTerminal(file.Fd())
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
	statusNode, err := staveRecordNode(semantic.NodeKey{Kind: "status", Entity: "summary", Slot: "status"}, "heading", "Lopper", status, staveStylePrimary, nil)
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
		warningNode, err := staveRecordNode(semantic.NodeKey{Kind: "warning", Entity: fmt.Sprintf("%d", i), Slot: "main"}, "alert", "Warning", safeDisplay(warning, ascii), staveStyleAdvisory, nil)
		if err != nil {
			return semantic.Tree{}, err
		}
		children = append(children, warningNode)
	}
	if showHelp {
		helpNode, err := staveRecordNode(semantic.NodeKey{Kind: "help", Entity: "summary", Slot: "footer"}, "status", "Help", "Commands: / filter | : command | arrows navigate | Enter open | r refresh | q quit", staveStyleAdvisory, nil)
		if err != nil {
			return semantic.Tree{}, err
		}
		children = append(children, helpNode)
	}
	return staveApplicationTree(children, baseStatus)
}

func staveInteractiveTree(view summaryReportView, sorted, deps []summaryDependencyView, state summaryState, totalPages int, ascii bool, interaction staveSummaryInteraction) (semantic.Tree, error) {
	width, height := staveInteractiveDimensions(interaction.viewport)
	selected := clampStaveRow(interaction.selectedRow, len(deps))
	detailDep, hasDetail := staveSelectedDetail(view, state.selectedDependency)
	if !hasDetail && interaction.focusPane == "detail" {
		interaction.focusPane = "summary"
	}

	headerNode, err := staveInteractiveHeader(view, state, totalPages, len(sorted), ascii)
	if err != nil {
		return semantic.Tree{}, err
	}
	children := []semantic.Node{headerNode}

	if interaction.help {
		return staveInteractiveHelp(children, interaction, width, height, ascii)
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
	children, err = appendStaveDependencyRows(children, deps, selected, rowBudget, ascii, interaction.focusPane == "summary")
	if err != nil {
		return semantic.Tree{}, err
	}
	children = append(children, detailNodes...)
	children, err = appendStaveWarningSummary(children, view.Warnings, width, ascii)
	if err != nil {
		return semantic.Tree{}, err
	}
	if len(children) > height {
		children = children[:height]
	}
	return staveApplicationTree(children, "Stave preview")
}

func staveInteractiveDimensions(viewport layout.Size) (int, int) {
	width, height := viewport.Width, viewport.Height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	return width, height
}

func staveInteractiveHeader(view summaryReportView, state summaryState, totalPages, dependencyCount int, ascii bool) (semantic.Node, error) {
	separator := staveSeparator(ascii)
	header := fmt.Sprintf("Stave preview%spage %d/%d%s%d deps%s%d/page", separator, state.page, totalPages, separator, dependencyCount, separator, state.pageSize)
	if state.filter != "" {
		header += separator + "filter " + safeDisplay(state.filter, ascii)
	}
	if len(view.Warnings) > 0 {
		header += fmt.Sprintf("%s%d warnings", separator, len(view.Warnings))
	}
	return staveRecordNode(semantic.NodeKey{Kind: "status", Entity: "summary", Slot: "status"}, "heading", "Status", header, staveStylePrimary, nil)
}

func staveInteractiveHelp(children []semantic.Node, interaction staveSummaryInteraction, width, height int, ascii bool) (semantic.Tree, error) {
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

func appendStaveDependencyRows(children []semantic.Node, deps []summaryDependencyView, selected, rowBudget int, ascii, focused bool) ([]semantic.Node, error) {
	start, end := staveVisibleRows(len(deps), selected, rowBudget)
	for i := start; i < end; i++ {
		row, err := staveDependencyNode(deps[i], ascii, focused && i == selected)
		if err != nil {
			return nil, err
		}
		children = append(children, row)
	}
	return children, nil
}

func appendStaveWarningSummary(children []semantic.Node, warnings []string, width int, ascii bool) ([]semantic.Node, error) {
	if len(warnings) > 0 {
		warningName := "Warnings"
		if width < 32 {
			warningName = "Warn"
		}
		warningText := fmt.Sprintf("%d%s%s", len(warnings), staveSeparator(ascii), safeDisplay(warnings[0], ascii))
		warningNode, err := staveRecordNode(semantic.NodeKey{Kind: "warning-summary", Entity: "summary", Slot: "main"}, "alert", warningName, warningText, staveStyleAdvisory, nil)
		if err != nil {
			return nil, err
		}
		children = append(children, warningNode)
	}
	return children, nil
}

func staveDependencyNode(dep summaryDependencyView, ascii, selected bool) (semantic.Node, error) {
	content := fmt.Sprintf("%s%s%g%% used%s%d bytes waste", safeDisplay(dep.Language, ascii), staveSeparator(ascii), dep.UsedPercent, staveSeparator(ascii), dep.EstimatedUnusedBytes)
	name, style := safeDisplay(dep.Name, ascii), "status.success"
	if selected {
		name, style = "> "+name, staveStylePrimary
	}
	// Keys are logical identity, not display text. Sanitizing or ASCII-folding
	// them would make distinct dependencies collide before rendering.
	return staveRecordNode(semantic.NodeKey{Kind: "dependency", Entity: dep.Language + "/" + dep.Name, Slot: "main"}, "row", name, content, style, []semantic.ActionRef{{ID: staveActionOpen, Label: "Open dependency", Default: true}, {ID: staveActionApplyCodemod, Label: "Apply codemod"}})
}

func staveDetailNodes(dep summaryDependencyView, focused, ascii bool) ([]semantic.Node, error) {
	name := "Detail"
	style := staveStyleAdvisory
	if focused {
		name = "> Detail"
		style = staveStylePrimary
	}
	identity := safeDisplay(dep.Language+":"+dep.Name, ascii)
	lines := []struct {
		kind, name, value, style string
	}{
		{"detail", name, identity, style},
		{"detail-exports", "Exports", fmt.Sprintf("%d/%d used (%g%%)", dep.UsedExportsCount, dep.TotalExportsCount, dep.UsedPercent), staveStyleUnknown},
		{"detail-waste", "Waste", fmt.Sprintf("%d bytes estimated unused", dep.EstimatedUnusedBytes), staveStyleUnknown},
	}
	removalValue, removalStyle := "not a candidate", staveStyleUnknown
	if dep.RemovalCandidate != nil {
		removalValue, removalStyle = fmt.Sprintf("candidate score %g", dep.RemovalCandidate.Score), staveStyleAdvisory
	}
	lines = append(lines, struct {
		kind, name, value, style string
	}{"detail-removal", "Removal", removalValue, removalStyle})
	nodes := make([]semantic.Node, 0, len(lines))
	for _, line := range lines {
		node, err := staveRecordNode(semantic.NodeKey{Kind: line.kind, Entity: dep.Language + "/" + dep.Name, Slot: "detail"}, "status", line.name, line.value, line.style, nil)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func staveFeedbackNode(interaction staveSummaryInteraction, width int, ascii bool) (semantic.Node, error) {
	name, text, style := "Keys", staveKeyHint(width), staveStyleUnknown
	switch {
	case interaction.error != "":
		name, text, style = "Error", safeDisplay(interaction.error, ascii), "status.failure"
	case interaction.pendingConfirm != "":
		name, text, style = "Confirm", safeDisplay(interaction.pendingConfirm, ascii), staveStyleAdvisory
	case interaction.commandMode:
		name, text, style = "Command", safeDisplay(interaction.filterBuffer, ascii), staveStylePrimary
	case interaction.status != "":
		name, text, style = "Update", safeDisplay(interaction.status, ascii), "status.success"
	}
	return staveRecordNode(semantic.NodeKey{Kind: "feedback", Entity: "summary", Slot: "footer"}, "status", name, text, style, nil)
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
		node, err := staveRecordNode(semantic.NodeKey{Kind: "help", Entity: fmt.Sprintf("%d", i), Slot: "footer"}, "status", line.name, safeDisplay(line.text, ascii), staveStyleAdvisory, nil)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func staveRecordNode(key semantic.NodeKey, role, name, text, style string, actions []semantic.ActionRef) (semantic.Node, error) {
	key.AppNamespace, key.View = "lopper", "summary"
	return semantic.NewNode(semantic.NodeSpec{Key: &key, Generation: 1, Role: semantic.Role(role), Name: name, Description: text, Value: semantic.Value{Text: text, HasValue: true}, Style: semantic.StyleIntent{Role: style}, Flags: semantic.Flags{Visible: true}, Actions: actions})
}

func staveApplicationTree(children []semantic.Node, description string) (semantic.Tree, error) {
	root, err := semantic.NewNode(semantic.NodeSpec{Key: &semantic.NodeKey{AppNamespace: "lopper", View: "summary", Kind: "application", Entity: "summary", Slot: "main"}, Generation: 1, Role: "application", Name: "Lopper", Description: description, Style: semantic.StyleIntent{Role: staveStylePrimary}, Metadata: map[string]string{"layout.kind": "records"}, Flags: semantic.Flags{Visible: true}, Children: children, Actions: []semantic.ActionRef{{ID: staveActionQuit, Label: "Quit"}, {ID: staveActionRefresh, Label: "Refresh", Default: true}, {ID: staveActionSaveBaseline, Label: "Save baseline"}, {ID: staveActionCompareBaseline, Label: "Compare baseline"}}})
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
		tokens[role] = lopperThemeToken(string(role))
	}
	return theme.Theme{ID: "lopper-sap-ember-blight-loam", Version: "v1", Modes: map[theme.Mode]theme.TokenSet{theme.ModeAuto: tokens, theme.ModeDark: {}}, Densities: map[theme.Density]theme.TokenSet{theme.DensityComfortable: {}}, Glyphs: map[string]theme.GlyphSet{"render": {"truncation": {Unicode: "…", ASCII: "...", Width: 3}}}, Assets: map[string]theme.AssetRef{"brand.mark": {ID: "lopper.mark", Text: "L"}, "brand.mark.ascii": {ID: "lopper.mark.ascii", Text: "L"}, "brand.banner.terminal": {ID: "lopper.banner", Text: "SAP / EMBER / BLIGHT / LOAM"}}}
}

func lopperThemeToken(name string) theme.Value {
	switch {
	case strings.HasPrefix(name, "motion.duration"):
		return theme.Value{Kind: theme.KindDuration, Literal: "120ms"}
	case strings.HasPrefix(name, "motion.easing"), strings.HasPrefix(name, "type."):
		return theme.Value{Kind: theme.KindString, Literal: "terminal"}
	case strings.HasPrefix(name, "space."), strings.HasPrefix(name, "radius."), strings.HasPrefix(name, "elevation."):
		return theme.Value{Kind: theme.KindNumber, Literal: 2}
	default:
		return theme.Value{Kind: theme.KindColor, Literal: lopperThemeColor(name)}
	}
}

func lopperThemeColor(name string) string {
	// More specific palette rules take precedence over generic role colors.
	switch {
	case strings.Contains(name, "status.advisory.bg"):
		return "#f2c94c"
	case strings.Contains(name, "status.success.bg"):
		return "#35d08f"
	case strings.Contains(name, "action.destructive.bg"), strings.Contains(name, "status.failure.bg"):
		return "#f14c4c"
	case strings.Contains(name, "action.secondary.bg"), strings.Contains(name, "status.unknown.bg"):
		return "#d7dce2"
	case strings.HasSuffix(name, ".fg") && (strings.Contains(name, "action.") || strings.Contains(name, "status.")):
		return "#000000"
	case name == "domain.primary.fg":
		return "#000000"
	case name == "domain.primary.bg", strings.Contains(name, "action.primary.bg"):
		return "#35d08f"
	case strings.HasSuffix(name, ".bg"):
		return "#24313a"
	case strings.Contains(name, "link"), strings.Contains(name, "chart"):
		return "#88b5ff"
	case strings.Contains(name, "border"), strings.Contains(name, "focus"):
		return "#aebbc5"
	case strings.Contains(name, "surface"):
		return "#101418"
	default:
		return "#f2f6f8"
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
