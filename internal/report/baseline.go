package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

var ErrBaselineMissing = errors.New("baseline report is missing summary data")

func Load(path string) (Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Report{}, err
	}

	var rep Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return Report{}, err
	}

	return rep, nil
}

func ComputeSummary(dependencies []DependencyReport) *Summary {
	if len(dependencies) == 0 {
		return nil
	}

	summary := Summary{DependencyCount: len(dependencies)}
	for _, dep := range dependencies {
		summary.UsedExportsCount += dep.UsedExportsCount
		summary.TotalExportsCount += dep.TotalExportsCount
	}
	if summary.TotalExportsCount > 0 {
		summary.UsedPercent = (float64(summary.UsedExportsCount) / float64(summary.TotalExportsCount)) * 100
	}

	return &summary
}

func ApplyBaseline(current Report, baseline Report) (Report, error) {
	currentSummary := current.Summary
	if currentSummary == nil {
		currentSummary = ComputeSummary(current.Dependencies)
		current.Summary = currentSummary
	}

	baselineSummary := baseline.Summary
	if baselineSummary == nil {
		baselineSummary = ComputeSummary(baseline.Dependencies)
	}
	if baselineSummary == nil {
		return current, ErrBaselineMissing
	}
	if baselineSummary.TotalExportsCount == 0 {
		return current, fmt.Errorf("baseline total exports count is zero")
	}

	currentWaste, ok := WastePercent(currentSummary)
	if !ok {
		return current, fmt.Errorf("current report has no export totals")
	}
	baselineWaste, _ := WastePercent(baselineSummary)
	delta := currentWaste - baselineWaste
	current.WasteIncreasePercent = &delta

	return current, nil
}

func WastePercent(summary *Summary) (float64, bool) {
	if summary == nil {
		return 0, false
	}
	if summary.TotalExportsCount == 0 {
		return 0, false
	}
	return 100 - summary.UsedPercent, true
}
