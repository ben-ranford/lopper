package errutil

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"testing"
)

type errorCauses []error

func (errorCauses) Error() string     { return "multiple causes" }
func (e errorCauses) Unwrap() []error { return e }

func TestIsPureSentinelError(t *testing.T) {
	cleanup := errors.New("cleanup failed")
	cases := []struct {
		name      string
		err       error
		sentinels []error
		want      bool
	}{
		{"nil error", nil, []error{io.EOF}, false},
		{"no sentinels", io.EOF, nil, false},
		{"nil sentinel", io.EOF, []error{nil}, false},
		{"direct sentinel", io.EOF, []error{io.EOF}, true},
		{"wrapped sentinel", fmt.Errorf("read: %w", io.EOF), []error{io.EOF}, true},
		{"joined allowed sentinels", errors.Join(io.EOF, fs.ErrNotExist), []error{io.EOF, fs.ErrNotExist}, true},
		{"joined cleanup failure", errors.Join(io.EOF, cleanup), []error{io.EOF}, false},
		{"nested cleanup failure", fmt.Errorf("read: %w", errors.Join(io.EOF, errors.Join(fs.ErrNotExist, cleanup))), []error{io.EOF, fs.ErrNotExist}, false},
		{"nil causes", errorCauses{nil, nil}, []error{io.EOF}, false},
		{"empty causes", errorCauses{}, []error{io.EOF}, false},
		{"ignore nil cause", errorCauses{nil, io.EOF}, []error{nil, io.EOF}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPureSentinelError(tc.err, tc.sentinels...); got != tc.want {
				t.Fatalf("IsPureSentinelError() = %v, want %v", got, tc.want)
			}
		})
	}
}
