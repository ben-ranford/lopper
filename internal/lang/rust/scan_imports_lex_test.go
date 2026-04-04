package rust

import "testing"

func TestRustImportLexStateTracksCommentsAndStrings(t *testing.T) {
	state := rustImportLexState{rawHashCount: -1}

	state.consumeLine([]byte(`let quoted = "still \"quoted\"" // trailing`))
	if state.inString || state.blockCommentDepth != 0 || state.insideRawString() {
		t.Fatalf("expected quoted line to finish in code state, got %#v", state)
	}

	state.consumeLine([]byte("/* outer"))
	if state.blockCommentDepth != 1 {
		t.Fatalf("expected block comment depth 1, got %#v", state)
	}
	state.consumeLine([]byte("/* inner"))
	if state.blockCommentDepth != 2 {
		t.Fatalf("expected nested block comment depth 2, got %#v", state)
	}
	state.consumeLine([]byte("plain text inside comment"))
	if state.blockCommentDepth != 2 {
		t.Fatalf("expected plain comment text to keep depth 2, got %#v", state)
	}
	state.consumeLine([]byte("inner */"))
	if state.blockCommentDepth != 1 {
		t.Fatalf("expected nested block comment close to reduce depth, got %#v", state)
	}
	state.consumeLine([]byte("outer */"))
	if state.blockCommentDepth != 0 {
		t.Fatalf("expected block comment to close, got %#v", state)
	}

	state.consumeLine([]byte(`let raw = br##"`))
	if !state.insideRawString() || state.rawHashCount != 2 {
		t.Fatalf("expected to enter br## raw string, got %#v", state)
	}
	state.consumeLine([]byte("still raw"))
	if !state.insideRawString() {
		t.Fatalf("expected to remain inside raw string, got %#v", state)
	}
	state.consumeLine([]byte(`"##;`))
	if state.insideRawString() {
		t.Fatalf("expected raw string terminator to reset state, got %#v", state)
	}
}

func TestRustRawStringHelpers(t *testing.T) {
	hashCount, consumed, ok := parseRustRawStringStart([]byte(`br##"`), 0)
	if !ok || hashCount != 2 || consumed != len(`br##"`) {
		t.Fatalf("expected br## raw string start to parse, got hashCount=%d consumed=%d ok=%v", hashCount, consumed, ok)
	}

	if _, _, ok := parseRustRawStringStart([]byte(`prefixr#"`), len("prefix")); ok {
		t.Fatalf("expected identifier-prefixed raw string start to be rejected")
	}

	if !hasRustRawStringTerminator([]byte(`"##`), 0, 2) {
		t.Fatalf("expected matching raw string terminator")
	}
	if hasRustRawStringTerminator([]byte(`"#x`), 0, 2) {
		t.Fatalf("expected mismatched raw string terminator to be rejected")
	}
}
