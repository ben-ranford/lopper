package cpp

import (
	"slices"
	"testing"
)

func TestSplitCompileCommandPreservesQuotedArguments(t *testing.T) {
	tests := []struct {
		name          string
		command       string
		want          []string
		windowsSyntax bool
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
		{
			name:    "empty quoted argument",
			command: `c++ -I "" -Iinclude -c src.cpp`,
			want:    []string{"c++", "-I", "", "-Iinclude", "-c", "src.cpp"},
		},
		{
			name:          "unquoted UNC path",
			command:       `c++ -I\\server\share -c src/main.cpp`,
			want:          []string{"c++", `-I\\server\share`, "-c", "src/main.cpp"},
			windowsSyntax: true,
		},
		{
			name:          "quoted UNC path with spaces",
			command:       `c++ -I"\\server\shared include" -c src/main.cpp`,
			want:          []string{"c++", `-I\\server\shared include`, "-c", "src/main.cpp"},
			windowsSyntax: true,
		},
		{
			name:          "quoted Windows path ending in a separator",
			command:       `cl.exe -I"C:\third party\\" -c main.cpp`,
			want:          []string{"cl.exe", `-IC:\third party\`, "-c", "main.cpp"},
			windowsSyntax: true,
		},
		{
			name:          "escaped Windows quote",
			command:       `cl.exe -DNAME=\"demo\" -c main.cpp`,
			want:          []string{"cl.exe", `-DNAME="demo"`, "-c", "main.cpp"},
			windowsSyntax: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if args := splitCompileCommand(test.command, test.windowsSyntax); !slices.Equal(args, test.want) {
				t.Fatalf("splitCompileCommand() = %#v, want %#v", args, test.want)
			}
		})
	}
}

func TestSplitCompileCommandPreservesPOSIXQuotes(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{
			name:    "single quoted backslash",
			command: `c++ -DROOT='vendor\' -Iinclude -c main.cpp`,
			want:    []string{"c++", `-DROOT=vendor\`, "-Iinclude", "-c", "main.cpp"},
		},
		{
			name:    "single quote in double quoted token",
			command: `c++ -DROOT="vendor\'" -Iinclude -c main.cpp`,
			want:    []string{"c++", `-DROOT=vendor\'`, "-Iinclude", "-c", "main.cpp"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if args := splitCompileCommand(test.command, false); !slices.Equal(args, test.want) {
				t.Fatalf("splitCompileCommand() = %#v, want %#v", args, test.want)
			}
		})
	}
}

func TestCompileCommandEntryUsesWindowsQuotesForWindowsMetadata(t *testing.T) {
	tests := []struct {
		name  string
		entry compileCommandEntry
		want  []string
	}{
		{
			name: "drive directory",
			entry: compileCommandEntry{
				Directory: `C:\build`,
				Command:   `cl.exe -IC:\O'Brien\headers -I"C:\'shared include" -c C:\O'Brien\main.cpp`,
			},
			want: []string{"cl.exe", `-IC:\O'Brien\headers`, `-IC:\'shared include`, "-c", `C:\O'Brien\main.cpp`},
		},
		{
			name: "UNC file",
			entry: compileCommandEntry{
				File:    `\\server\share\main.cpp`,
				Command: `cl.exe -I\\server\O'Brien\headers -c \\server\O'Brien\main.cpp`,
			},
			want: []string{"cl.exe", `-I\\server\O'Brien\headers`, "-c", `\\server\O'Brien\main.cpp`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if args := test.entry.compileArgs(); !slices.Equal(args, test.want) {
				t.Fatalf("compileArgs() = %#v, want %#v", args, test.want)
			}
		})
	}
}
