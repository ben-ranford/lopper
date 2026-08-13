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
			model.interaction.filterBuffer = p.Text
			if p.Committed {
				model.interaction.commandMode = false
				model.interaction.status, model.interaction.error = applyStaveCommand(&model.interaction.summary, p.Text, model.view)
				model.interaction.help = model.interaction.summary.showHelp
			}
		}
	case event.Key:
		if p, ok := ev.Payload.(event.KeyPayload); ok {
			reduceStaveKey(&model, p)
		}
	case event.ActionInvoked:
		if p, ok := ev.Payload.(event.ActionInvokedPayload); ok {
			model.interaction.status = "Pending " + p.ActionID
			model.interaction.pendingCallID, model.interaction.pendingActionID = p.CallID, p.ActionID
			model.interaction.error = ""
			if p.ActionID == staveActionQuit {
				model.interaction.pendingConfirm = p.CallID
			}
		}
	case event.Diagnostic:
		if p, ok := ev.Payload.(event.DiagnosticPayload); ok {
			model.interaction.status = ""
			model.interaction.error = p.Message
		}
	case event.EffectResult:
		if p, ok := ev.Payload.(event.EffectResultPayload); ok {
			pendingAction := model.interaction.pendingActionID
			if p.CallID == "" || p.CallID != model.interaction.pendingCallID {
				break
			}
			model.interaction.pendingConfirm = ""
			model.interaction.pendingCallID, model.interaction.pendingActionID = "", ""
			if p.Status == "error" || p.Error != "" {
				model.interaction.error = p.Error
			} else {
				model.interaction.status = p.Status
				model.interaction.error = ""
				envelope, normalizeErr := normalizeOutcomeMap(p.Value)
				if normalizeErr != nil {
					model.interaction.error = "invalid action outcome: " + normalizeErr.Error()
					model.interaction.pendingCallID, model.interaction.pendingActionID = "", ""
					break
				}
				if envelope["version"] != "lopper.action-result/v1" || envelope["action"] != pendingAction {
					model.interaction.error = "invalid action outcome: version or action mismatch"
					model.interaction.pendingCallID, model.interaction.pendingActionID = "", ""
					break
				}
				if envelope["value"] == nil {
					model.interaction.error = "invalid action outcome: missing value"
					model.interaction.pendingCallID, model.interaction.pendingActionID = "", ""
					break
				}
				valueMap, valueOK := envelope["value"].(map[string]any)
				if !valueOK {
					model.interaction.error = "invalid action outcome: value must be an object"
					model.interaction.pendingCallID, model.interaction.pendingActionID = "", ""
					break
				}
				if validationErr := validateStaveOutcome(pendingAction, valueMap); validationErr != "" {
					model.interaction.error = "invalid action outcome: " + validationErr
					model.interaction.pendingCallID, model.interaction.pendingActionID = "", ""
					break
				}
				if opts, ok := valueMap["options"].(map[string]any); ok && model.opts != nil {
					if v, ok := opts["baselinePath"].(string); ok {
						model.opts.BaselinePath = v
					}
					if v, ok := opts["baselineStorePath"].(string); ok {
						model.opts.BaselineStorePath = v
					}
					if v, ok := opts["baselineKey"].(string); ok {
						model.opts.BaselineKey = v
					}
				}
				if envelope["version"] == "lopper.action-result/v1" && envelope["action"] == pendingAction {
					value, _ := envelope["value"].(map[string]any)
					model.interaction.status = staveActionStatus(pendingAction, value)
					if rawReport, ok := value["report"]; ok && model.view != nil {
						if decoded, decodeErr := decodeStaveReportOutcome(rawReport); decodeErr == nil {
							mapped := mapSummaryReportView(decoded)
							model.view = &mapped
						} else {
							model.interaction.error = "invalid action outcome: report decode failed"
							model.interaction.pendingCallID, model.interaction.pendingActionID = "", ""
							break
						}
					}
					if dep, ok := value["dependency"].(string); ok {
						if model.view == nil {
							model.interaction.status = ""
							model.interaction.error = "No data for dependency " + dep
							model.interaction.summary.selectedDependency = ""
							model.interaction.focusPane = "summary"
						} else if _, found := staveSelectedDetail(*model.view, dep); !found {
							model.interaction.status = ""
							model.interaction.error = "No data for dependency " + dep
							model.interaction.summary.selectedDependency = ""
							model.interaction.focusPane = "summary"
						} else {
							model.interaction.summary.selectedDependency = dep
							model.interaction.focusPane = "detail"
						}
					}
					if pendingAction == staveActionRefresh {
						clampStavePage(&model)
						clampStaveSelection(&model)
						if selected := model.interaction.summary.selectedDependency; selected != "" {
							if _, found := staveSelectedDetail(*model.view, selected); !found {
								model.interaction.summary.selectedDependency = ""
								model.interaction.focusPane = "summary"
							}
						}
					}
					if pendingAction == staveActionQuit {
						model.interaction.quit = true
					}
					if cmd, ok := value["command"].(string); ok {
						_, model.interaction.error = applyStaveCommand(&model.interaction.summary, cmd, model.view)
						if model.interaction.error != "" {
							model.interaction.status = ""
						}
					}
				}
			}
		}
	}
	return model, nil, nil
}

func validateStaveOutcome(actionID string, value map[string]any) string {
	str := func(k string) bool { v, ok := value[k].(string); return ok && strings.TrimSpace(v) != "" }
	boolValue := func(k string) bool { _, ok := value[k].(bool); return ok }
	boolTrue := func(k string) bool { v, ok := value[k].(bool); return ok && v }
	switch actionID {
	case staveActionOpen:
		if !str("dependency") {
			return "dependency missing"
		}
	case staveActionRefresh:
		if !boolTrue("refreshed") || value["report"] == nil {
			return "refresh payload incomplete"
		}
	case staveActionApplyCodemod:
		if !str("dependency") || !boolValue("applied") || value["report"] == nil {
			return "codemod payload incomplete"
		}
	case staveActionSaveBaseline:
		if !boolTrue("ok") || value["report"] == nil || value["options"] == nil {
			return "save payload incomplete"
		}
	case staveActionCompareBaseline:
		if !boolTrue("ok") || value["report"] == nil || value["options"] == nil {
			return "compare payload incomplete"
		}
	case "lopper.summary.filter.v1", "lopper.summary.sort.v1", "lopper.summary.page.v1", "lopper.summary.size.v1":
		if !str("command") {
			return "command missing"
		}
	}
	return ""
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
		if m, ok := args.(map[string]any); ok {
			if dep, ok := m["dependency"].(string); ok {
				return "Opened " + dep
			}
		}
		return "Opened"
	case "lopper.summary.sort.v1":
		if m, ok := args.(map[string]any); ok {
			return "Sorted by " + fmt.Sprint(m["value"])
		}
	case "lopper.summary.filter.v1":
		if m, ok := args.(map[string]any); ok {
			return "Filtered " + fmt.Sprint(m["value"])
		}
	case "lopper.summary.page.v1":
		if m, ok := args.(map[string]any); ok {
			return "Page " + fmt.Sprint(m["value"])
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

func reduceStaveKey(model *staveSummaryModel, p event.KeyPayload) {
	key := strings.ToLower(strings.TrimSpace(p.Key))
	ctrl := false
	for _, modifier := range p.Modifiers {
		if modifier == "ctrl" {
			ctrl = true
			break
		}
	}
	if key == "rune" && p.Rune != 0 {
		key = strings.ToLower(string(p.Rune))
	}
	if ctrl && (key == "c" || key == "d") {
		model.interaction.quit = true
		return
	}
	// Printable input in command mode belongs to the editor, even when the
	// rune is also a global shortcut (h/q/j/k/n/p).
	if model.interaction.commandMode && p.Key == "rune" && p.Rune != 0 {
		model.interaction.filterBuffer += string(p.Rune)
		return
	}
	if model.interaction.commandMode && key == "space" {
		model.interaction.filterBuffer += " "
		return
	}
	switch key {
	case "q", "quit", "ctrl+c":
		model.interaction.quit = true
	case "escape":
		if model.interaction.commandMode {
			model.interaction.commandMode = false
			model.interaction.filterBuffer = ""
			model.interaction.status = "cancelled"
		} else {
			model.interaction.quit = true
		}
	case "?", "h", "help":
		model.interaction.help = !model.interaction.help
		model.interaction.summary.showHelp = model.interaction.help
	case "enter":
		if model.interaction.commandMode {
			model.interaction.commandMode = false
		}
	case "/":
		model.interaction.commandMode = true
		model.interaction.filterBuffer = "filter "
	case ":":
		model.interaction.commandMode = true
		model.interaction.filterBuffer = ""
	case "r":
		model.interaction.status = "refresh"
	case "backspace", "delete":
		if model.interaction.commandMode {
			buffer := model.interaction.filterBuffer
			runes := []rune(buffer)
			if len(runes) > 0 {
				buffer = string(runes[:len(runes)-1])
			}
			model.interaction.filterBuffer = buffer
		}
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
	case "right", "next", "n":
		model.interaction.summary.page++
		clampStavePage(model)
	case "tab":
		switch {
		case model.interaction.summary.selectedDependency == "":
			model.interaction.focusPane = "summary"
			model.interaction.status = "Open a dependency before focusing detail"
			model.interaction.error = ""
		case model.interaction.focusPane == "summary":
			model.interaction.focusPane = "detail"
		default:
			model.interaction.focusPane = "summary"
		}
	default:
	}
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

func clampStavePage(model *staveSummaryModel) {
	if model.view != nil {
		clampSummaryPage(*model.view, &model.interaction.summary)
	}
}

func applyStaveCommand(s *summaryState, input string, target any) (string, string) {
	if strings.TrimSpace(input) == "" {
		return "", ""
	}
	if !applySummaryCommand(s, strings.TrimSpace(input), io.Discard) {
		return "", fmt.Sprintf("unknown command: %s", strings.TrimSpace(input))
	}
	if value, ok := target.(*summaryReportView); ok && value != nil {
		clampSummaryPage(*value, s)
	}
	return "ok", ""
}
