package cpp

import (
	"slices"
	"testing"
)

func TestSplitCompileCommandPreservesQuotedArguments(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{
			name:    "quoted paths",
			command: `c++ -I"third party/include" -c "src/main file.cpp"`,
			want:    []string{"c++", "-Ithird party/include", "-c", "src/main file.cpp"},
		},
		{
			name:    "escaped space",
			command: `c++ -Ithird\ party/include -c src/main.cpp`,
			want:    []string{"c++", "-Ithird party/include", "-c", "src/main.cpp"},
		},
		{
			name:    "escaped quote",
			command: `c++ -DNAME=\"demo\" -c src/main.cpp`,
			want:    []string{"c++", `-DNAME="demo"`, "-c", "src/main.cpp"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if args := splitCompileCommand(test.command); !slices.Equal(args, test.want) {
				t.Fatalf("splitCompileCommand() = %#v, want %#v", args, test.want)
			}
		})
	}
}
