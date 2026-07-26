package js

import (
	"context"
	"fmt"
	"sort"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	riskCodeDynamicLoader = "dynamic-loader"
	riskCodeNativeModule  = "native-module"
	riskCodeDeepGraph     = "deep-transitive-graph"
)

func assessRiskCues(ctx context.Context, repoPath, dependency, dependencyRootPath string, surface ExportSurface) (cues []report.RiskCue, warnings []string) {
	depRoot := dependencyRootPath
	if depRoot == "" {
		root, err := dependencyRoot(repoPath, dependency)
		if err != nil {
			return nil, []string{fmt.Sprintf("unable to assess risk cues for %q: %v", dependency, err)}
		}
		depRoot = root
	}

	root, validatedDepRoot, err := openValidatedRootNoFollow(depRoot)
	if err != nil {
		return nil, []string{fmt.Sprintf("unable to assess risk cues for %q: %v", dependency, err)}
	}
	defer closeRootAppendWarning(root, &warnings, fmt.Sprintf("failed to close dependency root after risk analysis for %q", dependency))

	pkg, warnings := loadDependencyPackageJSONFromRoot(root, validatedDepRoot)
	aggregator := newRiskCueAggregator(repoPath, dependency, validatedDepRoot, root, pkg, warnings)
	aggregator.addDynamicLoaderCue(surface.EntryPoints)
	aggregator.addNativeModuleCue(ctx)
	aggregator.addTransitiveDepthCue()
	return aggregator.finalize()
}

type riskCueAggregator struct {
	repoPath   string
	dependency string
	depRoot    string
	root       safeio.Root
	pkg        packageJSON
	cues       []report.RiskCue
	warnings   []string
}

func newRiskCueAggregator(repoPath, dependency, depRoot string, root safeio.Root, pkg packageJSON, warnings []string) *riskCueAggregator {
	return &riskCueAggregator{
		repoPath:   repoPath,
		dependency: dependency,
		depRoot:    depRoot,
		root:       root,
		pkg:        pkg,
		cues:       make([]report.RiskCue, 0, 3),
		warnings:   append([]string(nil), warnings...),
	}
}

func (a *riskCueAggregator) addDynamicLoaderCue(entrypoints []string) {
	a.cues, a.warnings = appendDynamicRiskCueWithinRoot(a.cues, a.warnings, a.dependency, a.root, a.depRoot, entrypoints)
}

func (a *riskCueAggregator) addNativeModuleCue(ctx context.Context) {
	a.cues, a.warnings = appendNativeRiskCueWithinRoot(ctx, a.cues, a.warnings, a.dependency, a.root, a.depRoot, a.pkg)
}

func (a *riskCueAggregator) addTransitiveDepthCue() {
	a.cues, a.warnings = appendDepthRiskCue(a.cues, a.warnings, a.repoPath, a.depRoot, a.pkg)
}

func (a *riskCueAggregator) finalize() ([]report.RiskCue, []string) {
	sort.Slice(a.cues, func(i, j int) bool {
		return a.cues[i].Code < a.cues[j].Code
	})
	return a.cues, a.warnings
}

func appendDynamicRiskCue(cues []report.RiskCue, warnings []string, dependency, depRoot string, entrypoints []string) (resultCues []report.RiskCue, resultWarnings []string) {
	root, validatedDepRoot, err := openValidatedRootNoFollow(depRoot)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("dynamic loader scan failed for %q: %v", dependency, err))
		return cues, warnings
	}
	defer closeRootAppendWarning(root, &resultWarnings, fmt.Sprintf("failed to close dependency root after dynamic loader scan for %q", dependency))
	return appendDynamicRiskCueWithinRoot(cues, warnings, dependency, root, validatedDepRoot, entrypoints)
}

func appendDynamicRiskCueWithinRoot(cues []report.RiskCue, warnings []string, dependency string, root safeio.Root, depRoot string, entrypoints []string) ([]report.RiskCue, []string) {
	cue, dynamicWarnings, err := buildDynamicLoaderRiskCueWithinRootWithWarnings(root, depRoot, entrypoints)
	warnings = append(warnings, dynamicWarnings...)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("dynamic loader scan failed for %q: %v", dependency, err))
		return cues, warnings
	}
	if cue != nil {
		cues = append(cues, *cue)
	}
	return cues, warnings
}

func appendNativeRiskCue(ctx context.Context, cues []report.RiskCue, warnings []string, dependency, depRoot string, pkg packageJSON) (resultCues []report.RiskCue, resultWarnings []string) {
	root, validatedDepRoot, err := openValidatedRootNoFollow(depRoot)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("native module scan failed for %q: %v", dependency, err))
		return cues, warnings
	}
	defer closeRootAppendWarning(root, &resultWarnings, fmt.Sprintf("failed to close dependency root after native module scan for %q", dependency))
	return appendNativeRiskCueWithinRoot(ctx, cues, warnings, dependency, root, validatedDepRoot, pkg)
}

func appendNativeRiskCueWithinRoot(ctx context.Context, cues []report.RiskCue, warnings []string, dependency string, root safeio.Root, depRoot string, pkg packageJSON) ([]report.RiskCue, []string) {
	cue, err := buildNativeModuleRiskCueWithinRoot(ctx, root, depRoot, pkg)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("native module scan failed for %q: %v", dependency, err))
		return cues, warnings
	}
	if cue != nil {
		cues = append(cues, *cue)
	}
	return cues, warnings
}

func appendDepthRiskCue(cues []report.RiskCue, warnings []string, repoPath, depRoot string, pkg packageJSON) ([]report.RiskCue, []string) {
	depth, depthWarnings := estimateTransitiveDepth(repoPath, depRoot, pkg)
	warnings = append(warnings, depthWarnings...)
	cue := buildTransitiveDepthRiskCueForDepth(depth)
	if cue != nil {
		cues = append(cues, *cue)
	}
	return cues, warnings
}
