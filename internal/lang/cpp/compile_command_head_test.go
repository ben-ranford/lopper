package cpp

import (
	"slices"
	"testing"
)

func TestSplitCompileCommandPreservesQuotedArguments(t *testing.T) {
	args := splitCompileCommand(`c++ -I"third party/include" -c "src/main file.cpp"`)
	if !slices.Equal(args, []string{"c++", "-Ithird party/include", "-c", "src/main file.cpp"}) {
		t.Fatalf("unexpected command arguments: %#v", args)
	}
}
