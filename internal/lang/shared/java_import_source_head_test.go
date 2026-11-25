package shared

import (
	"strings"
	"testing"
)

func TestJavaImportLexicalSourceTranslatesUnicodeEscapes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "repeated markers", raw: `Grade \uu0041`, want: "Grade A"},
		{name: "raw backslash pair", raw: `\\u0041`, want: `\\u0041`},
		{name: "translated backslash", raw: `\u005c\u006e`, want: `\n`},
		{name: "non-ASCII code unit", raw: `\u215c`, want: "\x00"},
		{name: "invalid hex", raw: `\u00xyz`, want: `\u00xyz`},
		{name: "short escape", raw: `\u0`, want: `\u0`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, _ := javaImportLexicalSource([]byte(test.raw))
			if string(got) != test.want {
				t.Errorf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestMaskJVMImportSourceMapsJavaUnicodeEscapesToRawBytes(t *testing.T) {
	const label = `Unit \u005c" label`
	const detail = `hello\u0020there`
	const note = `\u002f* read the label */`
	source := strings.Join([]string{
		"class Example {",
		`  String label = "` + label + `";`,
		note + ` String detail = \uu0022` + detail + `\u0022;`,
		"  int count = 2;",
		"}",
	}, "\r\n")
	want := source
	for _, masked := range []string{label, detail, note} {
		want = strings.Replace(want, masked, strings.Repeat(" ", len(masked)), 1)
	}
	input := []byte(source)
	if got := string(MaskJVMImportSource(input, "Example.java")); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if string(input) != source {
		t.Error("changed input bytes")
	}
}
