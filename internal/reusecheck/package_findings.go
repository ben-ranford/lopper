package reusecheck

import (
	"cmp"
	"go/token"
	"path/filepath"
)

// The private physical offset in each key prevents distinct same-line nodes
// from lending each other build coverage. Severity is proved separately.
type analysisFindings map[Finding]*analysisFindingSupport

type analysisFindingSupport struct {
	goal                       *sourceBuildPredicate
	blocking, observed         []*sourceBuildPredicate
	directBlocking, directSeen bool
}

func (a analysisFindings) include(group analysisGroup, findings []Finding, positions *token.FileSet) {
	files := make(map[string]*analysisBuildSupport)
	for file, support := range group.support {
		path := positions.PositionFor(file.Pos(), false).Filename
		files[filepath.ToSlash(path)] = support
	}
	for _, finding := range findings {
		key := finding
		key.Advisory = false
		if a[key] == nil {
			a[key] = &analysisFindingSupport{}
		}
		a[key].include(files[finding.Path], finding.Advisory)
	}
}

func (s *analysisFindingSupport) include(support *analysisBuildSupport, advisory bool) {
	if support == nil || support.direct {
		s.directSeen = true
		s.directBlocking = s.directBlocking || !advisory
	}
	if support == nil {
		return
	}
	s.goal = support.goal
	s.observed = append(s.observed, support.contexts...)
	if !advisory {
		s.blocking = append(s.blocking, support.contexts...)
	}
}

func (a analysisFindings) covered() []Finding {
	var findings []Finding
	for finding, support := range a {
		if coversAnalysisGoal(support.goal, support.blocking, support.directBlocking) {
			findings = append(findings, finding)
		} else if coversAnalysisGoal(support.goal, support.observed, support.directSeen) {
			finding.Advisory = true
			findings = append(findings, finding)
		}
	}
	return findings
}

func coversAnalysisGoal(goal *sourceBuildPredicate, contexts []*sourceBuildPredicate, direct bool) bool {
	if direct {
		return true
	}
	return goal != nil && len(contexts) != 0 && goal.implies(unionSourceBuildPredicates(contexts))
}

func analysisFindingLess(left, right Finding) bool {
	return cmp.Or(cmp.Compare(left.Path, right.Path), cmp.Compare(left.Line, right.Line),
		cmp.Compare(left.offset, right.offset), cmp.Compare(left.Rule, right.Rule),
		cmp.Compare(left.Helper, right.Helper), cmp.Compare(left.Function, right.Function)) < 0
}
