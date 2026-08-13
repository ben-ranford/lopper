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
	for _, name := range names {
		frame.Actions = append(frame.Actions, ParityAction{Name: name, Supported: true})
	}
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
	return frame, nil
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
	add := func(path, a, b string) { report.Violations = append(report.Violations, ParityDiff{path, a, b}) }
	if want.Page != got.Page {
		add("page", strconv.Itoa(want.Page), strconv.Itoa(got.Page))
	}
	if want.TotalPages != got.TotalPages {
		add("total_pages", strconv.Itoa(want.TotalPages), strconv.Itoa(got.TotalPages))
	}
	if want.PageSize != got.PageSize {
		add("page_size", strconv.Itoa(want.PageSize), strconv.Itoa(got.PageSize))
	}
	if want.Capabilities.Width != got.Capabilities.Width {
		add("capabilities.width", strconv.Itoa(want.Capabilities.Width), strconv.Itoa(got.Capabilities.Width))
	}
	if want.Capabilities.ASCII != got.Capabilities.ASCII {
		add("capabilities.ascii", fmt.Sprintf("%t", want.Capabilities.ASCII), fmt.Sprintf("%t", got.Capabilities.ASCII))
	}
	if want.Capabilities.Color != got.Capabilities.Color {
		add("capabilities.color", fmt.Sprintf("%t", want.Capabilities.Color), fmt.Sprintf("%t", got.Capabilities.Color))
	}
	if want.Capabilities.Interactive != got.Capabilities.Interactive {
		report.CapabilityGaps = append(report.CapabilityGaps, ParityDiff{"capabilities.interactive", fmt.Sprintf("%t", want.Capabilities.Interactive), fmt.Sprintf("%t (%s)", got.Capabilities.Interactive, "renderer capability gap")})
	}
	if len(want.Rows) != len(got.Rows) {
		add("rows.length", strconv.Itoa(len(want.Rows)), strconv.Itoa(len(got.Rows)))
	}
	for i := 0; i < len(want.Rows) && i < len(got.Rows); i++ {
		a, b := want.Rows[i], got.Rows[i]
		if a.Identity != b.Identity {
			add(fmt.Sprintf("rows[%d].identity", i), a.Identity, b.Identity)
		}
		if a.Language != b.Language {
			add(fmt.Sprintf("rows[%d].language", i), a.Language, b.Language)
		}
		if a.Name != b.Name {
			add(fmt.Sprintf("rows[%d].name", i), a.Name, b.Name)
		}
		if a.Waste != b.Waste {
			add(fmt.Sprintf("rows[%d].waste", i), strconv.FormatInt(a.Waste, 10), strconv.FormatInt(b.Waste, 10))
		}
		if a.Used != b.Used {
			add(fmt.Sprintf("rows[%d].used", i), fmt.Sprintf("%g", a.Used), fmt.Sprintf("%g", b.Used))
		}
	}
	if strings.Join(want.Warnings, "\x00") != strings.Join(got.Warnings, "\x00") {
		add("warnings", strings.Join(want.Warnings, "|"), strings.Join(got.Warnings, "|"))
	}
	gotActions := map[string]ParityAction{}
	for _, action := range got.Actions {
		gotActions[action.Name] = action
	}
	for _, action := range want.Actions {
		other, ok := gotActions[action.Name]
		if !ok || action.Supported != other.Supported {
			reason := "legacy action is not exposed by the Stave preview"
			if ok {
				reason = other.GapReason
				if reason == "" && !other.Supported {
					reason = "Stave preview does not support this action"
				}
			}
			report.CapabilityGaps = append(report.CapabilityGaps, ParityDiff{"actions." + action.Name, fmt.Sprintf("supported=%t", action.Supported), reason})
		}
	}
	wantActions := map[string]bool{}
	for _, action := range want.Actions {
		wantActions[action.Name] = true
	}
	for _, action := range got.Actions {
		if !wantActions[action.Name] && action.Supported {
			report.CapabilityGaps = append(report.CapabilityGaps, ParityDiff{"actions." + action.Name, "unexpected", "supported"})
		}
	}
	return report
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
