package app

import (
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestPRReviewVersionRowsIgnoreOnlySemverPrefixAliases(t *testing.T) {
	tests := []struct {
		name, ecosystem, base, head, evidence string
		wantRows                              int
	}{
		{name: "lowercase prefix", ecosystem: "npm", base: "1.2.0", head: "v1.2.0"},
		{name: "uppercase prefix", ecosystem: "npm", base: "1.2.0", head: "V1.2.0"},
		{name: "prefix case", ecosystem: "npm", base: "v1.2.0", head: "V1.2.0"},
		{name: "repeated invalid prefix", ecosystem: "npm", base: "vV1.2.0", head: "V1.2.0", wantRows: 1, evidence: "version ordering was not inferred"},
		{name: "build identity", ecosystem: "npm", base: "1.2.0+one", head: "1.2.0+two", wantRows: 1, evidence: "version precedence is equal; artifact identities differ"},
		{name: "invalid alias", ecosystem: "npm", base: "release", head: "vrelease", wantRows: 1, evidence: "version ordering was not inferred"},
		{name: "pypi compatibility", ecosystem: "pypi", base: "1.2.0", head: "v1.2.0", wantRows: 1, evidence: "version precedence is equal; artifact identities differ"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := report.Report{Dependencies: []report.DependencyReport{prReviewTestDependency("demo", tt.ecosystem, tt.base, 10, 90, false)}}
			head := report.Report{Dependencies: []report.DependencyReport{prReviewTestDependency("demo", tt.ecosystem, tt.head, 10, 90, false)}}
			rows := prReviewVersionRows(base, head, prReviewCategoryVersionChanged)
			if len(rows) != tt.wantRows {
				t.Fatalf("version rows = %#v, want %d", rows, tt.wantRows)
			}
			if tt.wantRows > 0 && !slices.Contains(rows[0].Evidence, tt.evidence) {
				t.Fatalf("version evidence = %#v, want %q", rows[0].Evidence, tt.evidence)
			}
		})
	}
}
