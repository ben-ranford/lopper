package reusecheck

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestClosureExceptionsDoNotCoverSiblings(t *testing.T) {
	header, report := compactReportClosure()
	collection := collectionFunctionLiteral("trimmed")
	blank := strings.Replace(report, "func(", "func _(", 1)
	firstMethod := strings.Replace(report, "func(", "func (First) build(", 1)
	secondMethod := strings.Replace(report, "func(", "func (Second) build(", 1)
	_, returned, _ := strings.Cut(report, "return ")
	reportLiteral := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(returned), "}"))
	initializer := "func init() { var name string; var measured s.DependencyStats; _ = " + reportLiteral + " }"
	for _, source := range []string{
		header + "var builders = []any{" + report + ", " + report + "}",
		header + "var _, _ = " + report + ", " + report,
		header + "var _ = " + report + "; var _ = " + report,
		header + blank + "; " + blank,
		header + "type First struct{}; type Second struct{}; " + firstMethod + "; " + secondMethod,
		header + initializer + "; " + initializer,
		header + "func outer() { _ = " + report + "; _ = " + report + " }",
		header + "var builders = []any{func() { _ = " + report + "; _ = " + report + " }}",
		localCollectionSource("var builders = []any{" + collection + ", " + collection + "}"),
		localCollectionSource("func outer() { _ = " + collection + "; _ = " + collection + " }"),
	} {
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || len(findings) != 2 {
			t.Fatalf("sibling findings=%+v error=%v", findings, err)
		}
		first := findings[0]
		exceptions := reviewedClosureException(t, first, []byte(source))
		if !Approved(first, []byte(source), exceptions) {
			t.Fatal("selected closure did not retain its exact exception identity")
		}
		if Approved(findings[1], []byte(source), exceptions) {
			t.Fatalf("one closure exception also approved its sibling: %+v", findings)
		}
	}
}

func TestMethodIdentityDoesNotInheritFunctionOwner(t *testing.T) {
	for name, owner := range collectionOwners {
		literal := strings.TrimSpace(collectionFunctionLiteral(name))
		method := strings.Replace(literal, "func(", "func (Holder) "+owner.function+"(", 1)
		source := localCollectionSource("type Holder struct{}; " + method)
		findings, err := Analyze(owner.owner, []byte(source))
		if err != nil || len(findings) != 1 || findings[0].Rule != owner.rule {
			t.Fatalf("method inherited canonical function exemption: %+v error=%v", findings, err)
		}
	}
}

func TestClosureIdentityUsesPhysicalFilePosition(t *testing.T) {
	header, literal := compactReportClosure()
	for _, source := range []string{
		header + "var builders = []any{" + literal + ", " + literal + "}",
		header + "var builders = []any{\n//line duplicate.go:1\n" + literal + ",\n//line duplicate.go:1\n" + literal + ",\n}",
	} {
		original, err := Analyze("z.go", []byte(source))
		if err != nil || len(original) != 2 || original[0].Function == original[1].Function {
			t.Fatalf("physical closure positions not distinct: %+v error=%v", original, err)
		}
		withSibling, err := AnalyzeSources(map[string][]byte{
			"a.go": []byte("package fixture\n" + strings.Repeat("\n", 100) + "var unrelated int"),
			"z.go": []byte(source),
		})
		if err != nil || !reflect.DeepEqual(original, withSibling) {
			t.Fatalf("unrelated file changed closure identities: first=%+v later=%+v error=%v", original, withSibling, err)
		}
	}
}

func TestNamedFunctionExceptionScopeRemainsIntact(t *testing.T) {
	header, closure := compactReportClosure()
	_, returned, _ := strings.Cut(closure, "return ")
	literal := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(returned), "}"))
	source := []byte(header + "func build(name string, measured s.DependencyStats) { _ = " + literal + "; _ = " + literal + " }")
	findings, err := Analyze("fixture.go", source)
	if err != nil || len(findings) != 2 || findings[0].Function != "build" || findings[1].Function != "build" {
		t.Fatalf("named function scope changed: %+v error=%v", findings, err)
	}
	exceptions := reviewedClosureException(t, findings[0], source)
	if !Approved(findings[0], source, exceptions) || !Approved(findings[1], source, exceptions) {
		t.Fatal("named function findings lost their shared function scope")
	}
}

func reviewedClosureException(t *testing.T, finding Finding, source []byte) []Exception {
	t.Helper()
	item := Exception{
		Path: finding.Path, Function: finding.Function, Rule: finding.Rule,
		SHA256: fmt.Sprintf("%x", sha256.Sum256(source)), Reason: "Reviewed fixture contract",
		Issue: "https://github.com/ben-ranford/lopper/issues/1615",
	}
	document, err := json.Marshal([]Exception{item})
	if err != nil {
		t.Fatal(err)
	}
	exceptions, err := ReadExceptions(bytes.NewReader(document))
	if err != nil {
		t.Fatal(err)
	}
	return exceptions
}

func compactReportClosure() (string, string) {
	header, body, _ := strings.Cut(mappingFixture, "func build")
	body = strings.Replace(body, `_ = s.BuildDependencyReportFromStats(name, "python", measured)`, "", 1)
	return header, "func" + strings.ReplaceAll(body, "\n", " ")
}
