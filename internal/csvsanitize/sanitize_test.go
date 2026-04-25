package csvsanitize

import (
	"reflect"
	"testing"
)

func TestEscapeLeadingFormula(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "empty", value: "", want: ""},
		{name: "safe", value: "dependency", want: "dependency"},
		{name: "equals", value: "=sum(A1:A2)", want: "'=sum(A1:A2)"},
		{name: "plus", value: "+cmd", want: "'+cmd"},
		{name: "minus", value: "-cmd", want: "'-cmd"},
		{name: "at", value: "@cmd", want: "'@cmd"},
		{name: "tab", value: "\tcmd", want: "'\tcmd"},
		{name: "carriage", value: "\rcmd", want: "'\rcmd"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := EscapeLeadingFormula(tc.value); got != tc.want {
				t.Fatalf("EscapeLeadingFormula(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestEscapeLeadingFormulaRow(t *testing.T) {
	t.Parallel()

	values := []string{"dependency", "=sum(A1:A2)", "@cmd", "\tcmd", "\rcmd"}
	want := []string{"dependency", "'=sum(A1:A2)", "'@cmd", "'\tcmd", "'\rcmd"}

	if got := EscapeLeadingFormulaRow(values); !reflect.DeepEqual(got, want) {
		t.Fatalf("EscapeLeadingFormulaRow(%v) = %v, want %v", values, got, want)
	}
}
