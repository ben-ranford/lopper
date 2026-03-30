package js

import (
	"fmt"
	"sort"

	"github.com/ben-ranford/lopper/internal/report"
)

const (
	riskCodeDynamicLoader = "dynamic-loader"
	riskCodeNativeModule  = "native-module"
	riskCodeDeepGraph     = "deep-transitive-graph"
)

func assessRiskCues(repoPath string, dependency string, dependencyRootPath string, surface ExportSurface) ([]report.RiskCue, []string) {
	depRoot := dependencyRootPath
	if depRoot == "" {
		root, err := dependencyRoot(repoPath, dependency)
		if err != nil {
			return nil, []string{fmt.Sprintf("unable to assess risk cues for %q: %v", dependency, err)}
		}
		depRoot = root
	}

	pkg, warnings := loadDependencyPackageJSON(depRoot)
	aggregator := newRiskCueAggregator(repoPath, dependency, depRoot, pkg, warnings)
	aggregator.addDynamicLoaderCue(surface.EntryPoints)
	aggregator.addNativeModuleCue()
	aggregator.addTransitiveDepthCue()
	return aggregator.finalize()
}

type riskCueAggregator struct {
	repoPath   string
	dependency string
	depRoot    string
	pkg        packageJSON
	cues       []report.RiskCue
	warnings   []string
}

func newRiskCueAggregator(repoPath, dependency, depRoot string, pkg packageJSON, warnings []string) *riskCueAggregator {
	return &riskCueAggregator{
		repoPath:   repoPath,
		dependency: dependency,
		depRoot:    depRoot,
		pkg:        pkg,
		cues:       make([]report.RiskCue, 0, 3),
		warnings:   append([]string(nil), warnings...),
	}
}

func (a *riskCueAggregator) addDynamicLoaderCue(entrypoints []string) {
	a.cues, a.warnings = appendDynamicRiskCue(a.cues, a.warnings, a.dependency, a.depRoot, entrypoints)
}

func (a *riskCueAggregator) addNativeModuleCue() {
	a.cues, a.warnings = appendNativeRiskCue(a.cues, a.warnings, a.dependency, a.depRoot, a.pkg)
}

func (a *riskCueAggregator) addTransitiveDepthCue() {
	a.cues, a.warnings = appendDepthRiskCue(a.cues, a.warnings, a.dependency, a.repoPath, a.depRoot, a.pkg)
}

func (a *riskCueAggregator) finalize() ([]report.RiskCue, []string) {
	sort.Slice(a.cues, func(i, j int) bool {
		return a.cues[i].Code < a.cues[j].Code
	})
	return a.cues, a.warnings
}

func appendDynamicRiskCue(cues []report.RiskCue, warnings []string, dependency string, depRoot string, entrypoints []string) ([]report.RiskCue, []string) {
	cue, err := buildDynamicLoaderRiskCue(depRoot, entrypoints)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("dynamic loader scan failed for %q: %v", dependency, err))
		return cues, warnings
	}
	if cue != nil {
		cues = append(cues, *cue)
	}
	return cues, warnings
}

func appendNativeRiskCue(cues []report.RiskCue, warnings []string, dependency string, depRoot string, pkg packageJSON) ([]report.RiskCue, []string) {
	cue, err := buildNativeModuleRiskCue(depRoot, pkg)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("native module scan failed for %q: %v", dependency, err))
		return cues, warnings
	}
	if cue != nil {
		cues = append(cues, *cue)
	}
	return cues, warnings
}

func appendDepthRiskCue(cues []report.RiskCue, warnings []string, dependency string, repoPath string, depRoot string, pkg packageJSON) ([]report.RiskCue, []string) {
	_ = dependency
	cue := buildTransitiveDepthRiskCue(repoPath, depRoot, pkg)
	if cue != nil {
		cues = append(cues, *cue)
	}
	return cues, warnings
}
