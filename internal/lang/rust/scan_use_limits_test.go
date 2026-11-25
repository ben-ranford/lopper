package rust

import (
	"strconv"
	"strings"
	"testing"
)

func TestRustUseGroupKeepsEntriesWithinWorkLimit(t *testing.T) {
	const limit = 512
	var clause strings.Builder
	clause.WriteString("serde::{")
	for index := range limit + 1 {
		clause.WriteString("Item" + strconv.Itoa(index) + ",")
	}
	clause.WriteString("}")
	entries := parseUseClause(clause.String())
	if len(entries) != limit || entries[0].Path != "serde::Item0" {
		t.Fatalf("expected %d ordered entries, got %d", limit, len(entries))
	}
}
