package ui

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/ben-ranford/lopper/internal/report"
	"io"
	"strings"

	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/effect"
	"github.com/ben-ranford/stave/event"
	"github.com/ben-ranford/stave/layout"
)

// staveSummaryInteraction is value-owned by the Stave model. shared is only a
// narrowly scoped authority for report data and action side effects.
type staveSummaryInteraction struct {
	summary         summaryState
	selectedRow     int
	focusPane       string
	commandMode     bool
	filterBuffer    string
	viewport        layout.Size
	help            bool
	status          string
	error           string
	pendingConfirm  string
	pendingCallID   string
	pendingActionID string
	quit            bool
}

type staveSummaryModel struct {
	// View and options are session-owned snapshots. Action services live only
	// in registry closures and never enter replayable model state.
	view        *summaryReportView
	opts        *Options
	cloneErr    error
	interaction staveSummaryInteraction
}

// MarshalJSON exposes only replayable value-owned state. The shared action
// bridge is intentionally omitted so checkpoints cannot serialize services,
// writers, analyzers, or mutable pointers.
func (m staveSummaryModel) MarshalJSON() ([]byte, error) { //nostyle:recvtype -- JSON must see this method when the generic model is held as a non-addressable value.
	i := m.interaction
	var view *report.Report
	if m.view != nil {
		v := report.Report{Dependencies: summaryViewDependenciesToReport(m.view.Dependencies), Warnings: append([]string(nil), m.view.Warnings...), UsageUncertainty: m.view.UsageUncertainty, Scope: m.view.Scope, Cache: m.view.Cache, EffectiveThresholds: m.view.EffectiveThresholds, EffectivePolicy: m.view.EffectivePolicy, BaselineComparison: m.view.BaselineComparison}
		view = &v
	}
	return json.Marshal(struct {
		Interaction any            `json:"interaction"`
		View        *report.Report `json:"view,omitempty"`
		Options     *Options       `json:"options,omitempty"`
	}{Interaction: struct {
		Filter, Sort                                                                                               string
		SummaryShowHelp                                                                                            bool `json:"summaryShowHelp"`
		Page, PageSize                                                                                             int
		SelectedDependency, FocusPane, FilterBuffer, Status, Error, PendingConfirm, PendingCallID, PendingActionID string
		SelectedRow, ViewportWidth, ViewportHeight                                                                 int
		CommandMode, Help, Quit                                                                                    bool
	}{i.summary.filter, string(i.summary.sortMode), i.summary.showHelp, i.summary.page, i.summary.pageSize, i.summary.selectedDependency, i.focusPane, i.filterBuffer, i.status, i.error, i.pendingConfirm, i.pendingCallID, i.pendingActionID, i.selectedRow, i.viewport.Width, i.viewport.Height, i.commandMode, i.help, i.quit}, View: view, Options: m.opts})
}

func newStaveSummaryModel(view *summaryReportView, opts *Options, initial summaryState) staveSummaryModel {
	model := staveSummaryModel{interaction: staveSummaryInteraction{summary: initial, focusPane: "summary", viewport: layout.Size{Width: 80, Height: 24}}}
	if view != nil {
		if cloned, err := cloneSummaryReportView(*view); err == nil {
			model.view = &cloned
		} else {
			model.cloneErr = err
		}
	}
	if opts != nil {
		clonedOpts := cloneSummaryOptions(*opts)
		model.opts = &clonedOpts
	}
	return model
}

func cloneStaveSummaryModel(m staveSummaryModel) (staveSummaryModel, error) {
	if m.view != nil {
		cloned, err := cloneSummaryReportView(*m.view)
		if err != nil {
			return staveSummaryModel{}, err
		}
		m.view = &cloned
	}
	if m.opts != nil {
		opts := cloneSummaryOptions(*m.opts)
		m.opts = &opts
	}
	return m, nil
}

func cloneSummaryOptions(opts Options) Options {
	if opts.Color != nil {
		color := *opts.Color
		opts.Color = &color
	}
	return opts
}

func hashStaveSummaryModel(m staveSummaryModel) ([32]byte, error) {
	i := m.interaction
	projection := struct {
		Summary struct {
			Filter, SortMode string
			Page, PageSize   int
			ShowHelp         bool
			Selected         string
		} `json:"summary"`
		SelectedRow     int
		FocusPane       string
		CommandMode     bool
		FilterBuffer    string
		ViewportWidth   int
		ViewportHeight  int
		Help            bool
		Status          string
		Error           string
		PendingConfirm  string
		PendingCallID   string
		PendingActionID string
		Quit            bool
		Options         struct {
			RepoPath, Language, Filter, Sort, BaselinePath, BaselineStorePath, BaselineKey string
			TopN, PageSize, Width                                                          int
			ASCII, UseStavePreview                                                         bool
			Color                                                                          *bool
		}
		View                report.Report
		HasView, HasOptions bool
	}{
		SelectedRow: i.selectedRow, FocusPane: i.focusPane, CommandMode: i.commandMode,
		FilterBuffer: i.filterBuffer, ViewportWidth: i.viewport.Width, ViewportHeight: i.viewport.Height,
		Help: i.help, Status: i.status, Error: i.error, PendingConfirm: i.pendingConfirm, PendingCallID: i.pendingCallID, PendingActionID: i.pendingActionID, Quit: i.quit,
	}
	if m.opts != nil {
		projection.HasOptions = true
		projection.Options = struct {
			RepoPath, Language, Filter, Sort, BaselinePath, BaselineStorePath, BaselineKey string
			TopN, PageSize, Width                                                          int
			ASCII, UseStavePreview                                                         bool
			Color                                                                          *bool
		}{m.opts.RepoPath, m.opts.Language, m.opts.Filter, m.opts.Sort, m.opts.BaselinePath, m.opts.BaselineStorePath, m.opts.BaselineKey, m.opts.TopN, m.opts.PageSize, m.opts.Width, m.opts.ASCII, m.opts.UseStavePreview, m.opts.Color}
	}
	if m.view != nil {
		projection.HasView = true
		projection.View = report.Report{Dependencies: summaryViewDependenciesToReport(m.view.Dependencies), Warnings: append([]string(nil), m.view.Warnings...), UsageUncertainty: m.view.UsageUncertainty, Scope: m.view.Scope, Cache: m.view.Cache, EffectiveThresholds: m.view.EffectiveThresholds, EffectivePolicy: m.view.EffectivePolicy, BaselineComparison: m.view.BaselineComparison}
	}
	projection.Summary.Filter, projection.Summary.SortMode = i.summary.filter, string(i.summary.sortMode)
	projection.Summary.Page, projection.Summary.PageSize = i.summary.page, i.summary.pageSize
	projection.Summary.ShowHelp, projection.Summary.Selected = i.summary.showHelp, i.summary.selectedDependency
	b, err := json.Marshal(projection)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(b), nil
}

func reduceStaveSummary(_ stave.ReduceContext, model staveSummaryModel, ev event.Event) (staveSummaryModel, []effect.Request, error) {
	switch ev.Kind {
	case event.Shutdown:
		model.interaction.quit = true
	case event.Resize:
		if p, ok := ev.Payload.(event.ResizePayload); ok {
			model.interaction.viewport = layout.Size{Width: maxInt(1, p.Width), Height: maxInt(1, p.Height)}
		}
	case event.Text:
		if p, ok := ev.Payload.(event.TextPayload); ok {
			reduceStaveText(&model, p)
		}
	case event.Key:
		if p, ok := ev.Payload.(event.KeyPayload); ok {
			reduceStaveKey(&model, p)
		}
	case event.ActionInvoked:
		if p, ok := ev.Payload.(event.ActionInvokedPayload); ok {
			reduceStaveActionInvoked(&model, p)
		}
	case event.Diagnostic:
		if p, ok := ev.Payload.(event.DiagnosticPayload); ok {
			model.interaction.status = ""
			model.interaction.error = p.Message
		}
	case event.EffectResult:
		if p, ok := ev.Payload.(event.EffectResultPayload); ok {
			reduceStaveEffectResult(&model, p)
		}
	}
	return model, nil, nil
}

func reduceStaveText(model *staveSummaryModel, payload event.TextPayload) {
	model.interaction.filterBuffer = payload.Text
	if !payload.Committed {
		return
	}
	model.interaction.commandMode = false
	model.interaction.status, model.interaction.error = applyStaveCommand(&model.interaction.summary, payload.Text, model.view)
	if model.interaction.status != "" && model.interaction.error == "" {
		clampStaveSlice(model)
	}
	model.interaction.help = model.interaction.summary.showHelp
}

func reduceStaveActionInvoked(model *staveSummaryModel, payload event.ActionInvokedPayload) {
	model.interaction.status = "Pending " + payload.ActionID
	model.interaction.pendingCallID, model.interaction.pendingActionID = payload.CallID, payload.ActionID
	model.interaction.error = ""
	if payload.ActionID == staveActionQuit {
		model.interaction.pendingConfirm = payload.CallID
	}
}

func reduceStaveEffectResult(model *staveSummaryModel, payload event.EffectResultPayload) {
	pendingAction, matched := stavePendingAction(model, payload.CallID)
	if !matched {
		return
	}
	if payload.Status == "error" || payload.Error != "" {
		model.interaction.error = payload.Error
		return
	}
	model.interaction.status, model.interaction.error = payload.Status, ""
	value, ok := validatedStaveEffectValue(pendingAction, payload.Value, model)
	if !ok {
		return
	}
	applyStaveEffectValue(model, pendingAction, value)
}

func stavePendingAction(model *staveSummaryModel, callID string) (string, bool) {
	if callID == "" || callID != model.interaction.pendingCallID {
		return "", false
	}
	pendingAction := model.interaction.pendingActionID
	model.interaction.pendingConfirm = ""
	model.interaction.pendingCallID, model.interaction.pendingActionID = "", ""
	return pendingAction, true
}

func validatedStaveEffectValue(actionID string, outcome any, model *staveSummaryModel) (map[string]any, bool) {
	envelope, err := normalizeOutcomeMap(outcome)
	if err != nil {
		model.interaction.error = "invalid action outcome: " + err.Error()
		return nil, false
	}
	if envelope["version"] != "lopper.action-result/v1" || envelope["action"] != actionID {
		model.interaction.error = "invalid action outcome: version or action mismatch"
		return nil, false
	}
	if envelope["value"] == nil {
		model.interaction.error = "invalid action outcome: missing value"
		return nil, false
	}
	value, ok := envelope["value"].(map[string]any)
	if !ok {
		model.interaction.error = "invalid action outcome: value must be an object"
		return nil, false
	}
	if validationErr := validateStaveOutcome(actionID, value); validationErr != "" {
		model.interaction.error = "invalid action outcome: " + validationErr
		return nil, false
	}
	updateStaveOutcomeOptions(model, value)
	return value, true
}

func updateStaveOutcomeOptions(model *staveSummaryModel, value map[string]any) {
	options, ok := value["options"].(map[string]any)
	if !ok || model.opts == nil {
		return
	}
	if v, ok := options["baselinePath"].(string); ok {
		model.opts.BaselinePath = v
	}
	if v, ok := options["baselineStorePath"].(string); ok {
		model.opts.BaselineStorePath = v
	}
	if v, ok := options["baselineKey"].(string); ok {
		model.opts.BaselineKey = v
	}
}

func applyStaveEffectValue(model *staveSummaryModel, actionID string, value map[string]any) {
	model.interaction.status = staveActionStatus(actionID, value)
	if !updateStaveOutcomeReport(model, actionID, value) {
		return
	}
	updateStaveOutcomeDependency(model, value)
	if failure, ok := value["failure"].(string); ok {
		model.interaction.status = ""
		model.interaction.error = failure
	}
	if actionID == staveActionRefresh {
		clampStaveSlice(model)
		clearMissingStaveDetail(model)
	}
	if actionID == staveActionQuit {
		model.interaction.quit = true
	}
	if command, ok := value["command"].(string); ok {
		_, model.interaction.error = applyStaveCommand(&model.interaction.summary, command, model.view)
		clampStaveSlice(model)
		if model.interaction.error != "" {
			model.interaction.status = ""
		}
	}
}

func updateStaveOutcomeReport(model *staveSummaryModel, actionID string, value map[string]any) bool {
	rawReport, ok := value["report"]
	if !ok || model.view == nil {
		return true
	}
	decoded, err := decodeStaveReportOutcome(rawReport)
	if err != nil {
		model.interaction.error = "invalid action outcome: report decode failed"
		return false
	}
	if actionID == staveActionApplyCodemod {
		dependency, _ := value["dependency"].(string)
		dep, found := resolveStaveOutcomeDependency(model, dependency)
		if !found {
			updateStaveOutcomeDependency(model, value)
			return false
		}
		if dep.Language != "" {
			dependency = dep.Language + ":" + dep.Name
		}
		applyReport := findCodemodApplyReport(decoded, dependency)
		if applyReport == nil {
			model.interaction.error = "invalid action outcome: codemod result missing"
			return false
		}
		cloned, err := cloneSummaryReportView(*model.view)
		if err != nil {
			model.interaction.error = "invalid action outcome: report clone failed"
			return false
		}
		mergeCodemodApplyReport(&cloned, dependency, applyReport)
		model.view = &cloned
		return true
	}
	mapped := mapSummaryReportView(decoded)
	model.view = &mapped
	return true
}

func updateStaveOutcomeDependency(model *staveSummaryModel, value map[string]any) {
	dependency, ok := value["dependency"].(string)
	if !ok {
		return
	}
	if dep, found := resolveStaveOutcomeDependency(model, dependency); found {
		model.interaction.summary.selectedDependency = dep.Language + ":" + dep.Name
		model.interaction.focusPane = "detail"
		return
	}
	model.interaction.status = ""
	model.interaction.error = "No data for dependency " + dependency
	model.interaction.summary.selectedDependency = ""
	model.interaction.focusPane = "summary"
}

func resolveStaveOutcomeDependency(model *staveSummaryModel, dependency string) (detailDependencyView, bool) {
	if model.view == nil {
		return detailDependencyView{}, false
	}
	languageID := ""
	if model.opts != nil {
		languageID = model.opts.Language
	}
	languageID, name := parseDependencyLanguage(languageID, dependency)
	return findSummaryDependencyDetail(model.view.Dependencies, languageID, name)
}

func clearMissingStaveDetail(model *staveSummaryModel) {
	selected := model.interaction.summary.selectedDependency
	if selected == "" || model.view == nil {
		return
	}
	if _, found := staveSelectedDetail(*model.view, selected); !found {
		model.interaction.summary.selectedDependency = ""
		model.interaction.focusPane = "summary"
	}
}

func validateStaveOutcome(actionID string, value map[string]any) string {
	str := func(k string) bool { v, ok := value[k].(string); return ok && strings.TrimSpace(v) != "" }
	boolValue := func(k string) bool { _, ok := value[k].(bool); return ok }
	boolTrue := func(k string) bool { v, ok := value[k].(bool); return ok && v }
	switch actionID {
	case staveActionOpen:
		return staveRequiredString(str, "dependency")
	case staveActionRefresh:
		return staveRequiredTrueReport(boolTrue, value, "refreshed", "refresh")
	case staveActionApplyCodemod:
		return staveCodemodOutcomeError(str, boolValue, value)
	case staveActionSaveBaseline:
		return staveRequiredTrueReportOptions(boolTrue, value, "save")
	case staveActionCompareBaseline:
		return staveRequiredTrueReportOptions(boolTrue, value, "compare")
	case "lopper.summary.filter.v1", "lopper.summary.sort.v1", "lopper.summary.page.v1", "lopper.summary.size.v1":
		return staveRequiredString(str, "command")
	}
	return ""
}

func staveRequiredString(hasValue func(string) bool, name string) string {
	if hasValue(name) {
		return ""
	}
	return name + " missing"
}

func staveRequiredTrueReport(hasTrue func(string) bool, value map[string]any, field, action string) string {
	if hasTrue(field) && value["report"] != nil {
		return ""
	}
	return action + " payload incomplete"
}

func staveCodemodOutcomeError(hasString, hasBool func(string) bool, value map[string]any) string {
	if hasString("dependency") && hasBool("applied") && value["report"] != nil {
		if _, hasFailure := value["failure"]; !hasFailure || hasString("failure") {
			return ""
		}
		return "codemod failure invalid"
	}
	return "codemod payload incomplete"
}

func staveRequiredTrueReportOptions(hasTrue func(string) bool, value map[string]any, action string) string {
	if hasTrue("ok") && value["report"] != nil && value["options"] != nil {
		return ""
	}
	return action + " payload incomplete"
}

func normalizeOutcomeMap(value any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	var envelope struct {
		Version    string          `json:"version"`
		Action     string          `json:"action"`
		Value      json.RawMessage `json:"value"`
		Report     json.RawMessage `json:"report"`
		Dependency string          `json:"dependency"`
		Command    string          `json:"command"`
		Applied    *bool           `json:"applied"`
		Refreshed  *bool           `json:"refreshed"`
		OK         *bool           `json:"ok"`
		Path       string          `json:"path"`
		Key        string          `json:"key"`
		Target     string          `json:"target"`
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&envelope); err != nil {
		return nil, err
	}
	return result, nil
}

func decodeStaveReportOutcome(value any) (report.Report, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return report.Report{}, err
	}
	var decoded report.Report
	if err := json.Unmarshal(data, &decoded); err != nil {
		return report.Report{}, err
	}
	return decoded, nil
}

func staveActionStatus(id string, args any) string {
	switch id {
	case staveActionRefresh:
		return "Refreshed"
	case staveActionOpen:
		if dependency, ok := staveStringStatusValue(args, "dependency"); ok {
			return "Opened " + dependency
		}
		return "Opened"
	case "lopper.summary.sort.v1":
		if value, ok := staveStatusValue(args, "value"); ok {
			return "Sorted by " + value
		}
	case "lopper.summary.filter.v1":
		if value, ok := staveStatusValue(args, "value"); ok {
			return "Filtered " + value
		}
	case "lopper.summary.page.v1":
		if value, ok := staveStatusValue(args, "value"); ok {
			return "Page " + value
		}
	case staveActionSaveBaseline:
		return "Baseline saved"
	case staveActionCompareBaseline:
		return "Baseline compared"
	case staveActionApplyCodemod:
		if m, ok := args.(map[string]any); ok {
			if applied, ok := m["applied"].(bool); ok && !applied {
				return "No codemod changes"
			}
		}
		return "Codemod applied"
	}
	return "Action complete"
}

func staveStatusValue(args any, field string) (string, bool) {
	values, ok := args.(map[string]any)
	if !ok {
		return "", false
	}
	value, ok := values[field]
	if !ok {
		return "", false
	}
	return fmt.Sprint(value), true
}

func staveStringStatusValue(args any, field string) (string, bool) {
	values, ok := args.(map[string]any)
	if !ok {
		return "", false
	}
	value, ok := values[field].(string)
	return value, ok
}

func reduceStaveKey(model *staveSummaryModel, p event.KeyPayload) {
	key := strings.ToLower(strings.TrimSpace(p.Key))
	ctrl := staveHasModifier(p.Modifiers, "ctrl")
	if key == "rune" && p.Rune != 0 {
		key = strings.ToLower(string(p.Rune))
	}
	if ctrl && (key == "c" || key == "d") {
		model.interaction.quit = true
		return
	}
	if reduceStaveCommandModeKey(model, key, p) {
		return
	}
	if reduceStaveGlobalKey(model, key) {
		return
	}
	reduceStaveNavigationKey(model, key)
}

func reduceStaveGlobalKey(model *staveSummaryModel, key string) bool {
	switch key {
	case "q", "quit", "ctrl+c":
		model.interaction.quit = true
		return true
	case "escape":
		if model.interaction.commandMode {
			model.interaction.commandMode = false
			model.interaction.filterBuffer = ""
			model.interaction.status = "cancelled"
		} else {
			model.interaction.quit = true
		}
		return true
	case "?", "h", "help":
		model.interaction.help = !model.interaction.help
		model.interaction.summary.showHelp = model.interaction.help
		return true
	case "enter":
		if model.interaction.commandMode {
			model.interaction.commandMode = false
		}
		return true
	case "/":
		model.interaction.commandMode = true
		model.interaction.filterBuffer = "filter "
		return true
	case ":":
		model.interaction.commandMode = true
		model.interaction.filterBuffer = ""
		return true
	case "r":
		model.interaction.status = "refresh"
		return true
	case "backspace", "delete":
		if model.interaction.commandMode {
			model.interaction.filterBuffer = staveTrimLastRune(model.interaction.filterBuffer)
		}
		return true
	default:
		return false
	}
}

func staveTrimLastRune(value string) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return value
	}
	return string(runes[:len(runes)-1])
}

func reduceStaveNavigationKey(model *staveSummaryModel, key string) {
	switch key {
	case "up", "k":
		if model.interaction.focusPane == "summary" && model.interaction.selectedRow > 0 {
			model.interaction.selectedRow--
		}
	case "down", "j":
		if model.interaction.focusPane == "summary" {
			model.interaction.selectedRow++
			clampStaveSelection(model)
		}
	case "left", "prev", "p":
		if model.interaction.summary.page > 1 {
			model.interaction.summary.page--
		}
		clampStaveSelection(model)
	case "right", "next", "n":
		model.interaction.summary.page++
		clampStaveSlice(model)
	case "tab":
		reduceStaveFocus(model)
	}
}

func reduceStaveFocus(model *staveSummaryModel) {
	if model.interaction.summary.selectedDependency == "" {
		model.interaction.focusPane = "summary"
		model.interaction.status = "Open a dependency before focusing detail"
		model.interaction.error = ""
		return
	}
	if model.interaction.focusPane == "summary" {
		model.interaction.focusPane = "detail"
		return
	}
	model.interaction.focusPane = "summary"
}

func staveHasModifier(modifiers []string, wanted string) bool {
	for _, modifier := range modifiers {
		if modifier == wanted {
			return true
		}
	}
	return false
}

func reduceStaveCommandModeKey(model *staveSummaryModel, key string, payload event.KeyPayload) bool {
	if !model.interaction.commandMode {
		return false
	}
	// Printable input in command mode belongs to the editor, even when the
	// rune is also a global shortcut (h/q/j/k/n/p).
	if payload.Key == "rune" && payload.Rune != 0 {
		model.interaction.filterBuffer += string(payload.Rune)
		return true
	}
	if key == "space" {
		model.interaction.filterBuffer += " "
		return true
	}
	return false
}

func clampStaveSelection(model *staveSummaryModel) {
	if model.view == nil {
		return
	}
	_, deps, _, _ := runSummaryDependencyPipeline(*model.view, model.interaction.summary)
	if len(deps) == 0 {
		model.interaction.selectedRow = 0
		return
	}
	if model.interaction.selectedRow >= len(deps) {
		model.interaction.selectedRow = len(deps) - 1
	}
}

func clampStaveSlice(model *staveSummaryModel) {
	clampStavePage(model)
	clampStaveSelection(model)
}

func clampStavePage(model *staveSummaryModel) {
	if model.view != nil {
		clampSummaryPage(*model.view, &model.interaction.summary)
	}
}

func applyStaveCommand(s *summaryState, input string, target any) (string, string) {
	if strings.TrimSpace(input) == "" {
		return "", ""
	}
	candidate := *s
	if !applySummaryCommand(&candidate, strings.TrimSpace(input), io.Discard) {
		return "", fmt.Sprintf("unknown command: %s", strings.TrimSpace(input))
	}
	if value, ok := target.(*summaryReportView); ok && value != nil {
		clampSummaryPage(*value, &candidate)
	}
	*s = candidate
	return "ok", ""
}
