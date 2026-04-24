package ruby

import "github.com/ben-ranford/lopper/internal/report"

func buildRubyDependencyProvenance(info rubyDependencySource) *report.DependencyProvenance {
	source := rubyDependencyProvenanceSource(info)
	if source == "" {
		return nil
	}
	return &report.DependencyProvenance{
		Source:     source,
		Confidence: rubyDependencyProvenanceConfidence(info),
		Signals:    rubyDependencyProvenanceSignals(info),
	}
}

func rubyDependencyProvenanceSource(info rubyDependencySource) string {
	kinds := 0
	source := ""
	if info.Rubygems {
		kinds++
		source = rubyDependencySourceRubygems
	}
	if info.Git {
		kinds++
		source = rubyDependencySourceGit
	}
	if info.Path {
		kinds++
		source = rubyDependencySourcePath
	}
	switch kinds {
	case 0:
		return ""
	case 1:
		return source
	default:
		return rubyDependencySourceBundler
	}
}

func rubyDependencyProvenanceConfidence(info rubyDependencySource) string {
	switch {
	case info.DeclaredLock || info.Git || info.Path:
		return "high"
	case info.Rubygems:
		return "medium"
	default:
		return ""
	}
}

func rubyDependencyProvenanceSignals(info rubyDependencySource) []string {
	signals := make([]string, 0, 4)
	if rubyDependencyProvenanceSource(info) == rubyDependencySourceBundler {
		if info.Git {
			signals = append(signals, rubyDependencySourceGit)
		}
		if info.Path {
			signals = append(signals, rubyDependencySourcePath)
		}
		if info.Rubygems {
			signals = append(signals, rubyDependencySourceRubygems)
		}
	}
	if info.DeclaredGemfile {
		signals = append(signals, gemfileName)
	}
	if info.DeclaredLock {
		signals = append(signals, gemfileLockName)
	}
	return signals
}
