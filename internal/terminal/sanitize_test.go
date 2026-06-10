package terminal

import "testing"

func TestSanitizeString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "empty", value: "", want: ""},
		{name: "plain ascii", value: "safe/path.js", want: "safe/path.js"},
		{name: "unicode", value: "src/cafe.js", want: "src/cafe.js"},
		{name: "newline", value: "line1\nline2", want: "line1\\x0aline2"},
		{name: "escape", value: "safe\x1b[31mred", want: "safe\\x1b[31mred"},
		{name: "unicode after control", value: "line1\ncaf\u00e9", want: "line1\\x0acaf\u00e9"},
		{name: "delete", value: "a\x7fb", want: "a\\x7fb"},
		{name: "valid c1 control", value: "a" + string(rune(0x85)) + "b", want: "a\\x85b"},
		{name: "replacement char after control", value: "a\n\ufffdb", want: "a\\x0a\ufffdb"},
		{name: "c1 control", value: string([]byte{'a', 0x80, 'b'}), want: "a\\x80b"},
		{name: "invalid utf8", value: string([]byte{'a', 0xff, 'b'}), want: string([]byte{'a', 0xff, 'b'})},
		{
			name:  "invalid utf8 after control",
			value: string([]byte{'a', '\n', 0xff, 'b'}),
			want:  string([]byte{'a', '\\', 'x', '0', 'a', 0xff, 'b'}),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := SanitizeString(tc.value); got != tc.want {
				t.Fatalf("SanitizeString(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestSanitizeStrings(t *testing.T) {
	t.Parallel()

	if got := SanitizeStrings(nil); len(got) != 0 {
		t.Fatalf("SanitizeStrings(nil) = %#v, want nil", got)
	}

	got := SanitizeStrings([]string{"safe", "line1\nline2"})
	want := []string{"safe", "line1\\x0aline2"}
	if len(got) != len(want) {
		t.Fatalf("SanitizeStrings length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SanitizeStrings()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
