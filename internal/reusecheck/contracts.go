// Package reusecheck detects narrowly specified implementations of owned helpers.
package reusecheck

const module = "github.com/ben-ranford/lopper/internal/"

// Templates are contracts, not a general AST similarity model. Statement order,
// literals, operations, return shape and mutation remain significant.
const collectionContracts = `package contracts
import "sort"
import "strings"
func exact(values []string) []string {
 if len(values) == 0 { return nil }
 items := append([]string(nil), values...)
 sort.Strings(items)
 unique := items[:1]
 for i := 1; i < len(items); i++ {
  if items[i] != items[i-1] { unique = append(unique, items[i]) }
 }
 return unique
}
func trimmed(values []string) []string {
 if len(values) == 0 { return nil }
 seen := make(map[string]struct{}, len(values))
 out := make([]string, 0, len(values))
 for _, value := range values {
  value = strings.TrimSpace(value)
  if value == "" { continue }
  if _, ok := seen[value]; ok { continue }
  seen[value] = struct{}{}
  out = append(out, value)
 }
 sort.Strings(out)
 return out
}
func keys(values map[string]struct{}) []string {
 if len(values) == 0 { return nil }
 items := make([]string, 0, len(values))
 for value := range values { items = append(items, value) }
 sort.Strings(items)
 return items
}
func union(values ...map[string]struct{}) []string {
 set := make(map[string]struct{})
 for _, value := range values {
  for dependency := range value { set[dependency] = struct{}{} }
 }
 if len(set) == 0 { return nil }
 dependencies := make([]string, 0, len(set))
 for dependency := range set { dependencies = append(dependencies, dependency) }
 sort.Strings(dependencies)
 return dependencies
}
`

type contract struct{ rule, helper, owner, function string }

var collectionOwners = map[string]contract{
	"exact":   {"sorted-unique-exact", "analysis.uniqueSorted", "internal/analysis/pipeline.go", "uniqueSorted"},
	"trimmed": {"sorted-unique-trimmed", "report.SortedUniqueTrimmedStrings", "internal/report/strings.go", "SortedUniqueTrimmedStrings"},
	"union":   {"sorted-dependency-union", "shared.SortedDependencyUnion", "internal/lang/shared/dependency_usage.go", "SortedDependencyUnion"},
	"keys":    {"sorted-set-keys", "shared.SortedKeys", "internal/lang/shared/dependency_usage.go", "SortedKeys"},
}

var reportFields = map[string]string{
	"UsedExportsCount": "UsedCount", "TotalExportsCount": "TotalCount", "UsedPercent": "UsedPercent",
	"TopUsedSymbols": "TopSymbols", "UsedImports": "UsedImports", "UnusedImports": "UnusedImports",
}
