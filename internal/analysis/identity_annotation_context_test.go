package analysis

import (
	"context"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestIdentityAnnotationStopsAfterCancellation(t *testing.T) {
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Name: "first", Language: "js"},
		{Name: "second", Language: "js"},
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	annotationContext := &identityAnnotationCancelContext{
		deadline: ctx.Deadline,
		done:     ctx.Done,
		value:    ctx.Value,
		err: func() error {
			if reportData.Dependencies[0].Identity != nil {
				cancel()
			}
			return ctx.Err()
		},
	}

	annotateDependencyIdentitiesWithContext(annotationContext, t.TempDir(), &reportData)

	if reportData.Dependencies[0].Identity == nil {
		t.Fatal("expected first dependency to be annotated before cancellation")
	}
	if reportData.Dependencies[1].Identity != nil {
		t.Fatal("annotated second dependency after cancellation")
	}
}

type identityAnnotationCancelContext struct {
	deadline func() (time.Time, bool)
	done     func() <-chan struct{}
	err      func() error
	value    func(any) any
}

func (c *identityAnnotationCancelContext) Deadline() (time.Time, bool) {
	return c.deadline()
}

func (c *identityAnnotationCancelContext) Done() <-chan struct{} {
	return c.done()
}

func (c *identityAnnotationCancelContext) Err() error {
	return c.err()
}

func (c *identityAnnotationCancelContext) Value(key any) any {
	return c.value(key)
}

func TestBuildDependencyIdentityStopsDuringEvidence(t *testing.T) {
	dep := report.DependencyReport{Name: "package", Language: "js"}
	evidence := []identityEvidence{
		{Name: "package", Ecosystem: "npm", Version: "1.0.0"},
		{Name: "package", Ecosystem: "npm", Version: "2.0.0"},
	}
	identity := buildDependencyIdentityWithContext(&catalogCancelContext{remaining: 2}, dep, evidence)
	if identity != nil {
		t.Fatalf("returned identity after cancellation during evidence: %+v", identity)
	}
}

func TestIdentityEvidenceLookupStopsDuringQualification(t *testing.T) {
	dep := report.DependencyReport{Name: "package", Language: "jvm"}
	index := identityIndex{identityKey(dep.Language, dep.Name): {
		{Name: "package", Namespace: "org.example"},
		{Name: "package", Namespace: "org.example"},
	}}
	if evidence := identityEvidenceForDependencyWithContext(&catalogCancelContext{remaining: 1}, index, dep); len(evidence) != 0 {
		t.Fatalf("returned evidence after cancellation during qualification: %+v", evidence)
	}
}
