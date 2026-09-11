package ui

import (
	"context"
	"testing"

	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/event"
)

func preparedLopperActionArgs(t *testing.T, prepared *stave.Prepared[staveSummaryModel], id action.ID, args any) any {
	t.Helper()
	snapshot, err := prepared.Session.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	preparedArgs, err := prepareLopperActionArgs(snapshot.Model, id, args)
	if err != nil {
		t.Fatal(err)
	}
	return preparedArgs
}

type staveTestActionCall struct {
	sessionID string
	callID    string
	confirm   bool
}

// completeLopperAction drives the public action/event contract used by the
// interactive adapter: invocation is announced first, then the matching
// effect result carries the exact typed outcome value. Keeping this in one
// helper prevents tests from accidentally asserting on handler side effects
// before the reducer has observed completion.
func completeLopperAction(ctx context.Context, t *testing.T, prepared *stave.Prepared[staveSummaryModel], id action.ID, args any, call staveTestActionCall) (staveActionResult, error) {
	t.Helper()
	pending := mustStaveActionEvent(t, event.ActionInvoked, event.ActionInvokedPayload{CallID: call.callID, ActionID: string(id), Arguments: args})
	if err := sendLopperEvent(ctx, prepared, pending); err != nil {
		return staveActionResult{}, err
	}
	// The interactive adapter injects the session's current baseline options
	// before invoking typed baseline/refresh actions. Keep the pending event's
	// user arguments honest while applying the same preparation to the direct
	// handler invocation used by this test helper.
	invokeArgs := completionActionArgs(prepared, id, args)
	result, invokeErr := invokeLopperActionWithCallID(ctx, prepared, id, invokeArgs, call.sessionID, call.confirm, call.callID)
	payload := event.EffectResultPayload{CallID: call.callID}
	if invokeErr != nil {
		payload.Status = "error"
		if result.Error != nil {
			payload.Error = result.Error.Message
		} else {
			payload.Error = invokeErr.Error()
		}
	} else {
		payload.Status = "done"
		if result.Outcome != nil {
			payload.Value = result.Outcome.Value
		}
	}
	if err := sendLopperEvent(ctx, prepared, mustStaveActionEvent(t, event.EffectResult, payload)); err != nil {
		return result, err
	}
	return result, invokeErr
}

func completionActionArgs(prepared *stave.Prepared[staveSummaryModel], id action.ID, args any) any {
	if id == action.ID(staveActionRefresh) || id == action.ID(staveActionSaveBaseline) || id == action.ID(staveActionCompareBaseline) {
		if input, ok := args.(map[string]any); ok {
			snapshot, err := prepared.Session.Snapshot()
			if err == nil && snapshot.Model.opts != nil {
				preparedInput := make(map[string]any, len(input)+3)
				for key, value := range input {
					preparedInput[key] = value
				}
				preparedInput["currentBaselinePath"] = snapshot.Model.opts.BaselinePath
				preparedInput["currentBaselineStore"] = snapshot.Model.opts.BaselineStorePath
				preparedInput["currentBaselineKey"] = snapshot.Model.opts.BaselineKey
				return preparedInput
			}
		}
	}
	return args
}
