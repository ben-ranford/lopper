package shared

import (
	"errors"
	"reflect"
	"testing"
)

func assertReadSourceFileFailure(t *testing.T, repo, path string, want error) {
	t.Helper()
	content, display, err := ReadSourceFile(repo, path)
	if !errors.Is(err, want) || !reflect.DeepEqual(content, []byte(nil)) || display != "" {
		t.Fatalf("read %q: content=%q display=%q err=%v, want %v", path, content, display, err, want)
	}
}
