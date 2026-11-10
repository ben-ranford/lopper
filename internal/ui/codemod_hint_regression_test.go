package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestCodemodHintRoundTripsDependencyWithoutOptions(t *testing.T) {
	for _, target := range []string{
		"js-ts:lodash", "js-ts:two words", "js-ts:two  spaces", "js-ts:dep --allow-dirty",
		"--confirm", "-y", "--allow-dirty", `js-ts:quote"name`, `js-ts:slash\name`,
		"js-ts:café", "js-ts:invalid\xff", "js-ts:control\x85", "js-ts:dep\t--allow-dirty", "js-ts:dep\n--confirm", "js-ts:dep\x1b[31m",
		"js-ts:dep\x00end", "js-ts:dep\u2028--allow-dirty", " leading and trailing ",
	} {
		t.Run(target, func(t *testing.T) { assertCodemodHintRoundTrip(t, target) })
	}
}

func assertCodemodHintRoundTrip(t *testing.T, target string) {
	t.Helper()
	var out bytes.Buffer
	if err := printCodemodActionHint(&out, []detailCodemodSuggestionView{{}}, target); err != nil {
		t.Fatal(err)
	}
	command := strings.TrimSuffix(strings.TrimPrefix(out.String(), "  - action: "), "\n")
	if strings.ContainsAny(command, "\n\r\t\x1b\x00") {
		t.Fatalf("unsafe control in hint: %q", command)
	}
	action, ok, err := parseSummaryAction(command, nil)
	if err != nil || !ok || action.dependency != target || !action.confirm || action.allowDirty {
		t.Fatalf("hint %q parsed as %#v, handled=%t err=%v", command, action, ok, err)
	}
	command = strings.TrimSuffix(command, " --confirm")
	action, ok, err = parseSummaryAction(command, nil)
	if err != nil || !ok || action.dependency != target || action.confirm || action.allowDirty {
		t.Fatalf("dependency granted options in %q: %#v, handled=%t err=%v", command, action, ok, err)
	}
}

func TestCodemodQuotedArgumentsRejectMalformedInput(t *testing.T) {
	for _, input := range []string{`apply-codemod "unterminated`, `apply-codemod "bad\q"`, `apply-codemod "dep"--confirm`} {
		t.Run(input, func(t *testing.T) {
			if _, ok, err := parseSummaryAction(input, nil); !ok || err == nil {
				t.Fatalf("expected handled parse error for %q, handled=%t err=%v", input, ok, err)
			}
		})
	}
}
