package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/capability"
	"github.com/ben-ranford/stave/semantic"
	"github.com/ben-ranford/stave/state"
)

type staveSummaryShared struct {
	summary *Summary
	opts    Options
}

// staveActionOutcome is the serializable boundary between domain action
// execution and UI reduction. UI code must not infer success from text.
type staveActionOutcome struct {
	Version string         `json:"version"`
	Action  string         `json:"action"`
	Value   map[string]any `json:"value,omitempty"`
}

type staveActionError struct {
	Version string `json:"version"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type staveActionResult struct {
	Outcome *staveActionOutcome
	Error   *staveActionError
}

type staveActionExecution struct {
	result staveActionResult
	err    error
}

func newLopperStaveProgram(summary *Summary, opts *Options, view *summaryReportView, initialState *summaryState) (stave.Program[staveSummaryModel], error) {
	if opts == nil {
		return stave.Program[staveSummaryModel]{}, fmt.Errorf("stave options snapshot is unavailable")
	}
	if initialState == nil {
		initialState = &summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}
	}
	if view == nil {
		return stave.Program[staveSummaryModel]{}, fmt.Errorf("stave report snapshot is unavailable")
	}
	serviceOpts := cloneSummaryOptions(*opts)
	shared := &staveSummaryShared{summary: summary, opts: serviceOpts}
	registry, err := lopperSummaryActions(shared)
	if err != nil {
		return stave.Program[staveSummaryModel]{}, err
	}
	initial := newStaveSummaryModel(view, opts, *initialState)
	if initial.cloneErr != nil {
		return stave.Program[staveSummaryModel]{}, initial.cloneErr
	}
	if opts.Width > 0 {
		initial.interaction.viewport.Width = opts.Width
	}
	return stave.Program[staveSummaryModel]{
		Initial: initial,
		Reduce:  reduceStaveSummary,
		View: func(vc stave.ViewContext, model staveSummaryModel) (semantic.Tree, error) {
			if model.view == nil {
				return semantic.Tree{}, fmt.Errorf("stave report snapshot is unavailable")
			}
			sorted, paged, normalized, total := runSummaryDependencyPipeline(*model.view, model.interaction.summary)
			if model.opts == nil {
				return semantic.Tree{}, fmt.Errorf("stave options snapshot is unavailable")
			}
			ascii := model.opts.ASCII || model.interaction.viewport.Width < 40 || vc.Capabilities.Unicode != capability.UnicodeFull
			interaction := model.interaction
			interaction.summary = normalized
			return staveTreeForInteraction(*model.view, sorted, paged, normalized, total, ascii, interaction)
		},
		Actions: registry,
		Theme:   lopperTheme(),
		ModelPolicy: state.ModelPolicy[staveSummaryModel]{
			Clone:    cloneStaveSummaryModel,
			Sanitize: func(m staveSummaryModel) (staveSummaryModel, error) { return m, nil },
			Hash:     hashStaveSummaryModel,
		},
	}, nil
}

func lopperSummaryActions(shared *staveSummaryShared) (*action.Registry, error) {
	r := action.NewRegistry()
	obj := func(id string, props string, required string) action.Schema {
		return action.Schema{ID: id, JSON: json.RawMessage(fmt.Sprintf(`{"type":"object","additionalProperties":false%s%s}`, props, required))}
	}
	currentOptionsProperties := `,"currentBaselinePath":{"type":"string"},"currentBaselineStore":{"type":"string"},"currentBaselineKey":{"type":"string"}`
	currentOptionsRequired := `,"currentBaselinePath","currentBaselineStore","currentBaselineKey"`
	empty := obj("lopper.empty.input", "", "")
	refreshInput := obj("lopper.refresh.input", `,"properties":{`+strings.TrimPrefix(currentOptionsProperties, ",")+`}`, `,"required":[`+strings.TrimPrefix(currentOptionsRequired, ",")+`]`)
	out := func(id string) action.Schema {
		fields := `"version":{"enum":["lopper.action-result/v1"]},"action":{"enum":["` + id + `"]}`
		required := `"version","action"`
		switch id {
		case staveActionRefresh:
			fields += `,"refreshed":{"type":"boolean"},"report":{"type":"object"}`
			required += `,"refreshed","report"`
		case staveActionOpen:
			fields += `,"dependency":{"type":"string","minLength":1}`
			required += `,"dependency"`
		case staveActionApplyCodemod:
			fields += `,"dependency":{"type":"string"},"applied":{"type":"boolean"},"report":{"type":"object"}`
			required += `,"dependency","applied","report"`
		case staveActionSaveBaseline:
			fields += `,"ok":{"type":"boolean"},"report":{"type":"object"},"path":{"type":"string"},"key":{"type":"string"},"options":{"type":"object","additionalProperties":false,"properties":{"baselinePath":{"type":"string"},"baselineStorePath":{"type":"string"},"baselineKey":{"type":"string"}},"required":["baselinePath","baselineStorePath","baselineKey"]}`
			required += `,"ok","report","path","key","options"`
		case staveActionCompareBaseline:
			fields += `,"ok":{"type":"boolean"},"report":{"type":"object"},"target":{"type":"string"},"options":{"type":"object","additionalProperties":false,"properties":{"baselinePath":{"type":"string"},"baselineStorePath":{"type":"string"},"baselineKey":{"type":"string"}},"required":["baselinePath","baselineStorePath","baselineKey"]}`
			required += `,"ok","report","target","options"`
		case "lopper.summary.filter.v1", "lopper.summary.sort.v1", "lopper.summary.page.v1", "lopper.summary.size.v1":
			fields += `,"value":{"type":"string"}`
			required += `,"value"`
		}
		return action.Schema{ID: "lopper.action.output", JSON: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{` + fields + `},"required":[` + required + `]}`)}
	}
	base := func(id, title string, in action.Schema, safety action.Safety, idem action.Idempotency) action.Definition {
		return action.Definition{ID: action.ID(id), Version: "1", Title: title, InputSchema: in, OutputSchema: out(id), Safety: safety, Idempotency: idem, Cancellable: id != staveActionQuit}
	}
	for _, item := range []struct {
		id, title string
		input     action.Schema
	}{{staveActionQuit, "Quit", empty}, {staveActionRefresh, "Refresh", refreshInput}} {
		if err := r.Register(base(item.id, item.title, item.input, action.ReadOnly, action.Idempotent), func(ctx context.Context, _ action.Call, raw any) (any, error) {
			// Refresh is a domain action, not merely a status update: rerun the
			// analyzer and replace the live report while retaining interaction
			// state (filter, sort, page, and page size).
			if item.id == staveActionRefresh {
				if shared.summary == nil {
					return nil, fmt.Errorf("refresh action is unavailable")
				}
				input, ok := raw.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("invalid refresh action input")
				}
				workingOpts, err := staveActionOptions(shared.opts, input)
				if err != nil {
					return nil, err
				}
				refreshed, err := shared.summary.analyseSummaryView(ctx, workingOpts)
				if err != nil {
					return nil, err
				}
				return map[string]any{"version": "lopper.action-result/v1", "action": item.id, "refreshed": true, "report": summaryViewToReport(refreshed)}, nil
			}
			return map[string]any{"version": "lopper.action-result/v1", "action": item.id}, nil
		}); err != nil {
			return nil, err
		}
	}
	open := base(staveActionOpen, "Open dependency", obj("lopper.open.input", `,"properties":{"dependency":{"type":"string","minLength":1}}`, `,"required":["dependency"]`), action.ReadOnly, action.Idempotent)
	if err := r.Register(open, func(ctx context.Context, _ action.Call, raw any) (any, error) {
		in, ok := raw.(map[string]any)
		if !ok || shared.summary == nil {
			return nil, fmt.Errorf("invalid open action input")
		}
		dep, _ := in["dependency"].(string)
		return map[string]any{"version": "lopper.action-result/v1", "action": string(staveActionOpen), "dependency": dep}, nil
	}); err != nil {
		return nil, err
	}
	codemod := base(staveActionApplyCodemod, "Apply codemod", obj("lopper.codemod.input", `,"properties":{"dependency":{"type":"string","minLength":1},"confirm":{"type":"boolean"},"allowDirty":{"type":"boolean"}}`, `,"required":["dependency","confirm","allowDirty"]`), action.Consequential, action.NonIdempotent)
	codemod.Confirmation = action.ConfirmationPolicy{Required: true, SingleUse: true}
	if err := r.Register(codemod, func(ctx context.Context, _ action.Call, raw any) (any, error) {
		in, ok := raw.(map[string]any)
		if !ok || shared.summary == nil {
			return nil, fmt.Errorf("invalid codemod action input")
		}
		dep, _ := in["dependency"].(string)
		confirm, _ := in["confirm"].(bool)
		dirty, _ := in["allowDirty"].(bool)
		if !confirm {
			return nil, fmt.Errorf("codemod apply requires --confirm")
		}
		if shared.summary.Actions == nil {
			return nil, fmt.Errorf("codemod apply is unavailable")
		}
		languageID, dependencyName := parseDependencyLanguage(shared.opts.Language, dep)
		result, err := shared.summary.Actions.ApplyCodemod(ctx, CodemodApplyRequest{RepoPath: shared.opts.RepoPath, Dependency: dependencyName, TopN: shared.opts.TopN, Language: languageID, AllowDirty: dirty})
		if err != nil {
			return nil, err
		}
		applyReport := findCodemodApplyReport(result, dep)
		if applyReport == nil {
			return nil, fmt.Errorf("no safe codemod apply results for %s", dep)
		}
		applied := applyReport.AppliedFiles > 0 || applyReport.AppliedPatches > 0
		return map[string]any{"version": "lopper.action-result/v1", "action": string(staveActionApplyCodemod), "dependency": dep, "applied": applied, "report": result}, nil
	}); err != nil {
		return nil, err
	}
	for _, item := range []struct {
		id, title string
		kind      summaryActionKind
	}{{staveActionSaveBaseline, "Save baseline", summaryActionSaveBaseline}, {staveActionCompareBaseline, "Compare baseline", summaryActionCompareBaseline}} {
		in := obj(item.id+".input", `,"properties":{"label":{"type":"string"},"key":{"type":"string"},"store":{"type":"string"},"file":{"type":"string"},"target":{"type":"string"}`+currentOptionsProperties+`}`, `,"required":[`+strings.TrimPrefix(currentOptionsRequired, ",")+`]`)
		kind := item.kind
		if err := r.Register(base(item.id, item.title, in, action.Reversible, action.Idempotent), func(ctx context.Context, _ action.Call, raw any) (any, error) {
			m, ok := raw.(map[string]any)
			if !ok || shared.summary == nil {
				return nil, fmt.Errorf("invalid %s action input", item.title)
			}
			if kind == summaryActionSaveBaseline && shared.summary.Actions == nil {
				return nil, fmt.Errorf("%s action is unavailable", strings.ToLower(item.title))
			}
			a := summaryAction{kind: kind}
			a.baselineLabel, _ = m["label"].(string)
			a.baselineKey, _ = m["key"].(string)
			a.baselineStorePath, _ = m["store"].(string)
			a.baselinePath, _ = m["file"].(string)
			a.baselineTarget, _ = m["target"].(string)
			workingOpts, optionsErr := staveActionOptions(shared.opts, m)
			if optionsErr != nil {
				return nil, optionsErr
			}
			if kind == summaryActionSaveBaseline {
				req, displayKey, buildErr := buildSummaryBaselineSaveRequest(workingOpts, a)
				if buildErr != nil {
					return nil, buildErr
				}
				result, path, runErr := shared.summary.Actions.SaveBaseline(ctx, req)
				if runErr != nil {
					return nil, runErr
				}
				return map[string]any{"version": "lopper.action-result/v1", "action": string(item.id), "ok": true, "report": result, "path": path, "key": displayKey, "options": map[string]any{"baselinePath": workingOpts.BaselinePath, "baselineStorePath": req.BaselineStorePath, "baselineKey": workingOpts.BaselineKey}}, nil
			}
			nextOpts, target, buildErr := buildSummaryBaselineCompareOptions(workingOpts, a)
			if buildErr != nil {
				return nil, buildErr
			}
			reportView, runErr := shared.summary.analyseSummaryView(ctx, nextOpts)
			if runErr != nil {
				return nil, runErr
			}
			return map[string]any{"version": "lopper.action-result/v1", "action": string(item.id), "ok": true, "report": summaryViewToReport(reportView), "target": target, "options": map[string]any{"baselinePath": nextOpts.BaselinePath, "baselineStorePath": nextOpts.BaselineStorePath, "baselineKey": nextOpts.BaselineKey}}, nil
		}); err != nil {
			return nil, err
		}
	}
	for _, item := range []struct{ id, title, command string }{{"lopper.summary.filter.v1", "Filter", "filter"}, {"lopper.summary.sort.v1", "Sort", "sort"}, {"lopper.summary.page.v1", "Page", "page"}, {"lopper.summary.size.v1", "Page size", "size"}} {
		id, title := item.id, item.title
		if err := r.Register(base(id, title, obj(id+".input", `,"properties":{"value":{"type":"string","minLength":1}}`, `,"required":["value"]`), action.Reversible, action.Idempotent), func(_ context.Context, _ action.Call, raw any) (any, error) {
			input, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid %s action input", title)
			}
			value, _ := input["value"].(string)
			probe := summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}
			if !applySummaryCommand(&probe, item.command+" "+value, io.Discard) {
				return nil, fmt.Errorf("invalid %s value", title)
			}
			return map[string]any{"version": "lopper.action-result/v1", "action": id, "value": value}, nil
		}); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func staveActionOptions(base Options, input map[string]any) (Options, error) {
	path, pathOK := input["currentBaselinePath"].(string)
	store, storeOK := input["currentBaselineStore"].(string)
	key, keyOK := input["currentBaselineKey"].(string)
	if !pathOK || !storeOK || !keyOK {
		return Options{}, fmt.Errorf("current baseline options are required")
	}
	base.BaselinePath = path
	base.BaselineStorePath = store
	base.BaselineKey = key
	return base, nil
}

func prepareLopperActionArgs(model staveSummaryModel, id action.ID, args any) (any, error) {
	if id != action.ID(staveActionRefresh) && id != action.ID(staveActionSaveBaseline) && id != action.ID(staveActionCompareBaseline) {
		return args, nil
	}
	if model.opts == nil {
		return nil, fmt.Errorf("stave options snapshot is unavailable")
	}
	input, ok := args.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("action %s arguments must be an object", id)
	}
	prepared := make(map[string]any, len(input)+3)
	for key, value := range input {
		prepared[key] = value
	}
	prepared["currentBaselinePath"] = model.opts.BaselinePath
	prepared["currentBaselineStore"] = model.opts.BaselineStorePath
	prepared["currentBaselineKey"] = model.opts.BaselineKey
	return prepared, nil
}

func invokeLopperAction(ctx context.Context, prepared *stave.Prepared[staveSummaryModel], id action.ID, args any, sessionID string, confirm bool) error {
	_, err := invokeLopperActionResult(ctx, prepared, id, args, sessionID, confirm)
	return err
}

func invokeLopperActionResult(ctx context.Context, prepared *stave.Prepared[staveSummaryModel], id action.ID, args any, sessionID string, confirm bool) (staveActionResult, error) {
	return invokeLopperActionWithCallID(ctx, prepared, id, args, sessionID, confirm, "")
}

func invokeLopperActionWithCallID(ctx context.Context, prepared *stave.Prepared[staveSummaryModel], id action.ID, args any, sessionID string, confirm bool, callID string) (staveActionResult, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return staveActionResult{}, err
	}
	if callID == "" {
		callID = fmt.Sprintf("lopper-%d", time.Now().UnixNano())
	}
	call := action.Call{CallID: callID, ActionID: id, Arguments: raw, SessionID: sessionID}
	def, ok := prepared.Actions.Definition(id)
	if !ok {
		return staveActionResult{Error: &staveActionError{Version: "lopper.action-result/v1", Code: "NOT_REGISTERED", Message: fmt.Sprintf("action %s is not registered", id)}}, fmt.Errorf("action %s is not registered", id)
	}
	if def.Confirmation.Required && confirm {
		c, err := action.NewConfirmation(sessionID, def, semantic.Target{}, raw, time.Now().Add(time.Minute))
		if err != nil {
			return staveActionResult{}, err
		}
		if err := prepared.Actions.IssueConfirmation(c); err != nil {
			return staveActionResult{}, err
		}
		call.Confirmation = &c
	}
	result := prepared.Actions.Invoke(ctx, call)
	if result.Error != nil {
		message := fmt.Sprintf("%s %s: %s", id, result.Error.Code, result.Error.Message)
		return staveActionResult{Error: &staveActionError{Version: "lopper.action-result/v1", Code: string(result.Error.Code), Message: result.Error.Message}}, fmt.Errorf("%s", message)
	}
	var value map[string]any
	if len(result.Output) > 0 {
		if err := json.Unmarshal(result.Output, &value); err != nil {
			return staveActionResult{}, fmt.Errorf("decode action %s output: %w", id, err)
		}
	}
	if value == nil {
		value = map[string]any{}
	}
	if strings.HasPrefix(string(id), "lopper.summary.") {
		if v, ok := value["value"].(string); ok {
			value["command"] = strings.TrimSuffix(strings.TrimPrefix(string(id), "lopper.summary."), ".v1") + " " + v
		}
	}
	return staveActionResult{Outcome: &staveActionOutcome{Version: "lopper.action-result/v1", Action: string(id), Value: map[string]any{"version": "lopper.action-result/v1", "action": string(id), "value": value}}}, nil
}

func startLopperAction(ctx context.Context, prepared *stave.Prepared[staveSummaryModel], id action.ID, args any, sessionID string, confirm bool, callID string) <-chan staveActionExecution {
	completed := make(chan staveActionExecution, 1)
	go func() {
		result, err := invokeLopperActionWithCallID(ctx, prepared, id, args, sessionID, confirm, callID)
		completed <- staveActionExecution{result: result, err: err}
	}()
	return completed
}
