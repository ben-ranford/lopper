package reusecheck

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestReviewedExceptionIsExactSourceScoped(t *testing.T) {
	source := []byte("package example")
	document := fmt.Sprintf(`[{"path":"example.go","function":"copy","rule":"sorted-set-keys","sha256":"%x","reason":"Preserve an audited compatibility contract","issue":"https://github.com/ben-ranford/lopper/issues/1615"}]`, sha256.Sum256(source))
	exceptions, err := ReadExceptions(strings.NewReader(document))
	if err != nil {
		t.Fatal(err)
	}
	finding := Finding{Path: "example.go", Function: "copy", Rule: "sorted-set-keys"}
	if !Approved(finding, source, exceptions) {
		t.Fatal("exact reviewed source rejected")
	}
	if Approved(finding, []byte("changed"), exceptions) {
		t.Fatal("edited source inherited exception")
	}
	finding.Function = "newCopy"
	if Approved(finding, source, exceptions) {
		t.Fatal("different function inherited exception")
	}
	for _, invalid := range []string{
		"{", "null", "[] []", `[{"unknown":true}]`,
		strings.Replace(document, "example.go", "../example.go", 1),
		strings.Replace(document, "example.go", "/example.go", 1),
		strings.Replace(document, "example.go", "example.txt", 1),
		strings.Replace(document, "example.go", `folder\\example.go`, 1),
		strings.Replace(document, `"copy"`, `" "`, 1),
		strings.Replace(document, "sorted-set-keys", "unknown-rule", 1),
		strings.Replace(document, "/issues/1615", "/issues/not-an-issue", 1),
		strings.Replace(document, "/issues/1615", "/issues/0", 1),
		strings.Replace(document, "/issues/1615", "/issues/1615#fragment", 1),
		strings.Replace(document, "Preserve an audited compatibility contract", " ", 1),
		strings.Replace(document, "https://github.com/ben-ranford/lopper/issues/1615", "", 1),
		strings.Replace(document, fmt.Sprintf("%x", sha256.Sum256(source)), "invalid", 1),
		strings.Replace(document, fmt.Sprintf("%x", sha256.Sum256(source)), fmt.Sprintf("%X", sha256.Sum256(source)), 1),
		strings.TrimSuffix(document, "]") + "," + strings.TrimPrefix(document, "["),
	} {
		if _, err := ReadExceptions(strings.NewReader(invalid)); err == nil {
			t.Fatalf("accepted invalid exception: %s", invalid)
		}
	}
}

func TestEmptyExceptionArrayAndReportRule(t *testing.T) {
	if exceptions, err := ReadExceptions(strings.NewReader("[]")); err != nil || len(exceptions) != 0 {
		t.Fatalf("exceptions=%v err=%v", exceptions, err)
	}
	if !knownRule("dependency-report-mapping") {
		t.Fatal("report rule rejected")
	}
}
