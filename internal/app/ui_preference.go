package app

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/ui"
	"github.com/ben-ranford/lopper/internal/uipreference"
)

func (a *App) prepareTUI(ctx context.Context, req TUIRequest, opts *ui.Options) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	interactive := a.UIInteractive
	if interactive == nil {
		interactive = func() bool { return ui.CanManagePreference(a.In, a.Out) }
	}
	if !interactive() {
		if req.UIPreference != "" {
			return false, fmt.Errorf("--ui-preference requires an interactive terminal outside CI")
		}
		return true, nil
	}
	store := a.Preferences
	if store == nil {
		store = &uipreference.Store{}
	}
	if req.UIPreference != "" {
		return a.manageUIPreference(ctx, store, req, opts)
	}
	if req.StaveExplicit || req.UseStavePreview {
		return true, nil
	}
	choice, err := store.Load()
	if err != nil {
		a.preferenceWarning("could not read UI preference; using current UI (use --ui-preference=ask to reset)", err)
		opts.UseStavePreview = false
		return true, nil
	}
	if choice != "" {
		return true, applyUIPreference(opts, choice)
	}
	return a.inviteUI(ctx, store, opts)
}

func (a *App) manageUIPreference(ctx context.Context, store uipreference.Storage, req TUIRequest, opts *ui.Options) (bool, error) {
	if req.UIPreference == uipreference.Ask {
		if err := store.Clear(); err != nil {
			return false, fmt.Errorf("could not reset UI preference: %w", err)
		}
		if req.StaveExplicit {
			a.preferenceWarning("repository configuration controls the UI for this launch", nil)
			return true, nil
		}
		return a.inviteUI(ctx, store, opts)
	}
	if req.UIPreference != uipreference.Stave && req.UIPreference != uipreference.Legacy {
		return false, fmt.Errorf("invalid UI preference %q", req.UIPreference)
	}
	a.saveUIPreference(store, req.UIPreference)
	if req.StaveExplicit {
		a.preferenceWarning("repository configuration overrides the personal UI preference for this launch", nil)
		return true, nil
	}
	return true, applyUIPreference(opts, req.UIPreference)
}

func (a *App) inviteUI(ctx context.Context, store uipreference.Storage, opts *ui.Options) (bool, error) {
	eligible := a.UIEligible
	if eligible == nil {
		eligible = func() bool { return ui.CanOfferPreference(a.In, a.Out) }
	}
	if !eligible() {
		opts.UseStavePreview = false
		return true, nil
	}
	prompt := a.UIPrompt
	if prompt == nil {
		prompt = func(ctx context.Context) (string, error) { return ui.PromptPreference(ctx, a.In, a.Out) }
	}
	choice, err := prompt(ctx)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if choice == "" {
		opts.UseStavePreview = false
		return true, nil
	}
	a.saveUIPreference(store, choice)
	return true, applyUIPreference(opts, choice)
}

func (a *App) saveUIPreference(store uipreference.Storage, choice string) {
	if err := store.Save(choice); err != nil {
		a.preferenceWarning("UI choice applies this session but was not remembered", err)
	}
}

func (a *App) preferenceWarning(message string, err error) {
	if a.Out == nil {
		return
	}
	if err != nil {
		if _, writeErr := fmt.Fprintf(a.Out, "Warning: %s: %v\n", message, err); writeErr != nil {
			return
		}
		return
	}
	if _, writeErr := fmt.Fprintln(a.Out, message); writeErr != nil {
		return
	}
}

func applyUIPreference(opts *ui.Options, choice string) error {
	overrides := featureflags.Overrides{Disable: []string{"stave-tui-preview"}}
	if choice == uipreference.Stave {
		overrides = featureflags.Overrides{Enable: []string{"stave-tui-preview"}}
	}
	features, err := opts.Features.WithOverrides(overrides)
	if err != nil {
		return err
	}
	opts.Features = features
	opts.UseStavePreview = choice == uipreference.Stave
	return nil
}
