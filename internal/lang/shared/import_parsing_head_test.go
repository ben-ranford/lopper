package shared

import (
	"strings"
	"testing"
)

func TestStripBlockCommentsPreservesTripleQuotedText(t *testing.T) {
	content := []byte(`val text = """
a regular " quote and /* literal marker */
"""
/* ordinary comment */
import com.acme.Widget
`)

	lines := strings.Split(string(StripBlockComments(content)), "\n")
	if lines[0] != `val text = """` || lines[1] != `a regular " quote and /* literal marker */` || lines[2] != `"""` {
		t.Fatalf("expected triple-quoted text to remain unchanged, got %#v", lines[:3])
	}
	if strings.Contains(lines[3], "ordinary comment") || lines[4] != "import com.acme.Widget" {
		t.Fatalf("expected only the ordinary comment to be stripped, got %#v", lines)
	}
}

func TestStripBlockCommentsPreservesEscapedQuoteMarkers(t *testing.T) {
	content := []byte(`val text = "escaped \" quote and /* literal marker */"
/* ordinary comment */
import com.acme.Widget
`)

	lines := strings.Split(string(StripBlockComments(content)), "\n")
	if lines[0] != `val text = "escaped \" quote and /* literal marker */"` {
		t.Fatalf("expected escaped quoted text to remain unchanged, got %q", lines[0])
	}
	if strings.TrimSpace(lines[1]) != "" || lines[2] != "import com.acme.Widget" {
		t.Fatalf("expected only the ordinary comment to be stripped, got %#v", lines)
	}
}
