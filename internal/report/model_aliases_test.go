package report

import (
	"reflect"
	"testing"

	"github.com/ben-ranford/lopper/internal/report/model"
)

func TestReportModelAliasesExposeModelTypes(t *testing.T) {
	assertSameType[Report, model.Report](t)
	assertSameType[DependencyReport, model.DependencyReport](t)
	assertSameType[Summary, model.Summary](t)
	assertSameType[BaselineComparison, model.BaselineComparison](t)
	assertSameType[RemovalCandidate, model.RemovalCandidate](t)
	assertSameType[ReachabilityConfidence, model.ReachabilityConfidence](t)
	assertSameType[EffectivePolicy, model.EffectivePolicy](t)
}

func assertSameType[A, B any](t *testing.T) {
	t.Helper()

	var left A
	var right B
	leftType := reflect.TypeOf(left)
	rightType := reflect.TypeOf(right)
	if leftType != rightType {
		t.Fatalf("alias type = %v, want %v", leftType, rightType)
	}
}
