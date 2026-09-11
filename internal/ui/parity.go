package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ben-ranford/stave/semantic"
)

type ParityFrame struct {
	Rows                       []ParityRow
	Warnings                   []string
	Page, TotalPages, PageSize int
	Capabilities               ParityCapabilities
	Actions                    []ParityAction
}
type ParityRow struct {
	Identity, Language, Name string
	Waste                    int64
	Used                     float64
}
type ParityCapabilities struct {
	Width                     int
	ASCII, Color, Interactive bool
}
type ParityAction struct {
	Name      string
	Supported bool
	GapReason string
}
type ParityDiff struct{ Path, Want, Got string }
type ParityReport struct{ Violations, CapabilityGaps []ParityDiff }

// LegacyParityProjection is derived from the actual display view and state.
func LegacyParityProjection(view summaryDisplayView, state summaryState, totalPages int, caps ParityCapabilities) ParityFrame {
	deps := view.Dependencies
	rows := make([]ParityRow, 0, len(deps))
	for _, dep := range deps {
		rows = append(rows, ParityRow{Identity: dep.Language + ":" + dep.Name, Language: dep.Language, Name: dep.Name, Waste: dep.EstimatedUnusedBytes, Used: dep.UsedPercent})
	}
	actions := []ParityAction{{Name: staveActionQuit, Supported: true}, {Name: staveActionRefresh, Supported: true}, {Name: staveActionOpen, Supported: true}, {Name: staveActionApplyCodemod, Supported: true}, {Name: staveActionSaveBaseline, Supported: true}, {Name: staveActionCompareBaseline, Supported: true}}
	return ParityFrame{Rows: rows, Warnings: append([]string(nil), view.Warnings...), Page: state.page, TotalPages: totalPages, PageSize: state.pageSize, Capabilities: caps, Actions: actions}
}

// StaveParityProjection reads the actual Stave snapshot, not the source model.
func StaveParityProjection(tree semantic.Tree, caps ParityCapabilities) (ParityFrame, error) {
	root := tree.Snapshot().Root
	frame := ParityFrame{Capabilities: caps}
	frame.Actions = staveParityActions(root)
	staveParityHeader(&frame, root)
	staveParityChildren(&frame, root)
	return frame, nil
}

func staveParityActions(root semantic.Node) []ParityAction {
	actionNames := map[string]struct{}{}
	var walkActions func(semantic.Node)
	walkActions = func(node semantic.Node) {
		for _, action := range node.Actions() {
			actionNames[string(action.ID)] = struct{}{}
		}
		for _, child := range node.Children() {
			walkActions(child)
		}
	}
	walkActions(root)
	names := make([]string, 0, len(actionNames))
	for name := range actionNames {
		names = append(names, name)
	}
	sort.Strings(names)
	actions := make([]ParityAction, 0, len(names))
	for _, name := range names {
		actions = append(actions, ParityAction{Name: name, Supported: true})
	}
	return actions
}

func staveParityHeader(frame *ParityFrame, root semantic.Node) {
	rootContent := root.Description()
	if rootContent == "" {
		rootContent = root.Value().Text
	}
	separator := " | "
	if !strings.Contains(rootContent, separator) && strings.Contains(rootContent, " • ") {
		separator = " • "
	}
	parts := strings.Split(rootContent, separator)
	if len(parts) >= 1 {
		pageParts := strings.Fields(strings.TrimPrefix(parts[0], "page "))
		if len(pageParts) > 0 {
			pair := strings.Split(pageParts[0], "/")
			if len(pair) == 2 {
				frame.Page = parseParityInt(pair[0])
				frame.TotalPages = parseParityInt(pair[1])
			}
		}
		for _, part := range parts[1:] {
			if strings.HasSuffix(part, " page size") {
				frame.PageSize = parseParityInt(strings.TrimSuffix(part, " page size"))
			}
		}
	}
}

func staveParityChildren(frame *ParityFrame, root semantic.Node) {
	separator := staveParitySeparator(root)
	for _, child := range root.Children() {
		content := child.Description()
		if content == "" {
			content = child.Value().Text
		}
		if child.Role() == "alert" {
			frame.Warnings = append(frame.Warnings, content)
			continue
		}
		if child.Role() != "row" {
			continue
		}
		parts := strings.SplitN(content, separator, 3)
		if len(parts) < 3 {
			continue
		}
		used := parseParityFloat(strings.TrimSuffix(strings.TrimSpace(parts[1]), "% used"))
		waste := parseParityInt64(strings.TrimSuffix(strings.TrimSpace(parts[2]), " bytes waste"))
		frame.Rows = append(frame.Rows, ParityRow{Identity: parts[0] + ":" + child.Name(), Language: parts[0], Name: child.Name(), Used: used, Waste: waste})
	}
}

func staveParitySeparator(root semantic.Node) string {
	content := root.Description()
	if content == "" {
		content = root.Value().Text
	}
	if !strings.Contains(content, " | ") && strings.Contains(content, " • ") {
		return " • "
	}
	return " | "
}

func parseParityInt(value string) int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}

func parseParityInt64(value string) int64 {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func parseParityFloat(value string) float64 {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func CompareParity(want, got ParityFrame) ParityReport {
	report := ParityReport{}
	compareParityPagination(&report, want, got)
	compareParityCapabilities(&report, want.Capabilities, got.Capabilities)
	compareParityRows(&report, want.Rows, got.Rows)
	compareParityWarnings(&report, want.Warnings, got.Warnings)
	compareParityActions(&report, want.Actions, got.Actions)
	return report
}

func addParityViolation(report *ParityReport, path, want, got string) {
	report.Violations = append(report.Violations, ParityDiff{path, want, got})
}

func compareParityPagination(report *ParityReport, want, got ParityFrame) {
	if want.Page != got.Page {
		addParityViolation(report, "page", strconv.Itoa(want.Page), strconv.Itoa(got.Page))
	}
	if want.TotalPages != got.TotalPages {
		addParityViolation(report, "total_pages", strconv.Itoa(want.TotalPages), strconv.Itoa(got.TotalPages))
	}
	if want.PageSize != got.PageSize {
		addParityViolation(report, "page_size", strconv.Itoa(want.PageSize), strconv.Itoa(got.PageSize))
	}
}

func compareParityCapabilities(report *ParityReport, want, got ParityCapabilities) {
	if want.Width != got.Width {
		addParityViolation(report, "capabilities.width", strconv.Itoa(want.Width), strconv.Itoa(got.Width))
	}
	compareParityCapabilityBool(report, "ascii", want.ASCII, got.ASCII)
	compareParityCapabilityBool(report, "color", want.Color, got.Color)
	if want.Interactive != got.Interactive {
		report.CapabilityGaps = append(report.CapabilityGaps, ParityDiff{"capabilities.interactive", fmt.Sprintf("%t", want.Interactive), fmt.Sprintf("%t (%s)", got.Interactive, "renderer capability gap")})
	}
}

func compareParityCapabilityBool(report *ParityReport, name string, want, got bool) {
	if want != got {
		addParityViolation(report, "capabilities."+name, fmt.Sprintf("%t", want), fmt.Sprintf("%t", got))
	}
}

func compareParityRows(report *ParityReport, want, got []ParityRow) {
	if len(want) != len(got) {
		addParityViolation(report, "rows.length", strconv.Itoa(len(want)), strconv.Itoa(len(got)))
	}
	for index := 0; index < len(want) && index < len(got); index++ {
		compareParityRow(report, index, want[index], got[index])
	}
}

func compareParityRow(report *ParityReport, index int, want, got ParityRow) {
	prefix := fmt.Sprintf("rows[%d].", index)
	if want.Identity != got.Identity {
		addParityViolation(report, prefix+"identity", want.Identity, got.Identity)
	}
	if want.Language != got.Language {
		addParityViolation(report, prefix+"language", want.Language, got.Language)
	}
	if want.Name != got.Name {
		addParityViolation(report, prefix+"name", want.Name, got.Name)
	}
	if want.Waste != got.Waste {
		addParityViolation(report, prefix+"waste", strconv.FormatInt(want.Waste, 10), strconv.FormatInt(got.Waste, 10))
	}
	if want.Used != got.Used {
		addParityViolation(report, prefix+"used", fmt.Sprintf("%g", want.Used), fmt.Sprintf("%g", got.Used))
	}
}

func compareParityWarnings(report *ParityReport, want, got []string) {
	if strings.Join(want, "\x00") != strings.Join(got, "\x00") {
		addParityViolation(report, "warnings", strings.Join(want, "|"), strings.Join(got, "|"))
	}
}

func compareParityActions(report *ParityReport, want, got []ParityAction) {
	gotActions := parityActionsByName(got)
	report.CapabilityGaps = append(report.CapabilityGaps, missingParityActions(want, gotActions)...)
	report.CapabilityGaps = append(report.CapabilityGaps, unexpectedParityActions(want, got)...)
}

func parityActionsByName(actions []ParityAction) map[string]ParityAction {
	gotActions := map[string]ParityAction{}
	for _, action := range actions {
		gotActions[action.Name] = action
	}
	return gotActions
}

func missingParityActions(want []ParityAction, got map[string]ParityAction) []ParityDiff {
	gaps := make([]ParityDiff, 0)
	for _, action := range want {
		other, ok := got[action.Name]
		if !ok || action.Supported != other.Supported {
			gaps = append(gaps, ParityDiff{"actions." + action.Name, fmt.Sprintf("supported=%t", action.Supported), parityActionGapReason(other, ok)})
		}
	}
	return gaps
}

func parityActionGapReason(action ParityAction, found bool) string {
	if !found {
		return "legacy action is not exposed by the Stave preview"
	}
	if action.GapReason != "" {
		return action.GapReason
	}
	return "Stave preview does not support this action"
}

func unexpectedParityActions(want, got []ParityAction) []ParityDiff {
	wantActions := map[string]bool{}
	for _, action := range want {
		wantActions[action.Name] = true
	}
	gaps := make([]ParityDiff, 0)
	for _, action := range got {
		if !wantActions[action.Name] && action.Supported {
			gaps = append(gaps, ParityDiff{"actions." + action.Name, "unexpected", "supported"})
		}
	}
	return gaps
}

// EqualWithKnownGaps is the explicit oracle for the preview's documented
// one-shot/action limitations. Unknown, missing, or duplicate capability gaps
// remain failures so improvements and regressions both require an explicit
// contract update.
func (r *ParityReport) EqualWithKnownGaps(known map[string]bool) bool {
	if len(r.Violations) != 0 {
		return false
	}
	seen := make(map[string]bool, len(r.CapabilityGaps))
	for _, gap := range r.CapabilityGaps {
		if !known[gap.Path] || seen[gap.Path] {
			return false
		}
		seen[gap.Path] = true
	}
	return len(seen) == len(known)
}
