package workspace

import (
	"errors"
	"testing"
)

func TestJoinCloseError(t *testing.T) {
	t.Run("ignores nil close error", func(t *testing.T) {
		base := errors.New("base")
		err := error(base)
		joinCloseError(&err, nil)
		if !errors.Is(err, base) {
			t.Fatalf("expected base error to remain, got %v", err)
		}
	})

	t.Run("sets close error when target is nil", func(t *testing.T) {
		closeErr := errors.New("close")
		var err error
		joinCloseError(&err, closeErr)
		if !errors.Is(err, closeErr) {
			t.Fatalf("expected close error to be set, got %v", err)
		}
	})

	t.Run("joins close error when target already set", func(t *testing.T) {
		base := errors.New("base")
		closeErr := errors.New("close")
		err := error(base)
		joinCloseError(&err, closeErr)
		if !errors.Is(err, base) || !errors.Is(err, closeErr) {
			t.Fatalf("expected joined error to include both causes, got %v", err)
		}
	})
}
