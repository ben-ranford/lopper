package report

import "sort"

// SortByStringKeys sorts items in place by the supplied keys in priority order.
// Keys are compared literally, without normalization. Equal keys have no stable
// ordering guarantee. Nil and empty slices are unchanged.
func SortByStringKeys[T any](items []T, keys ...func(T) string) {
	sort.Slice(items, func(i, j int) bool {
		for _, key := range keys {
			left, right := key(items[i]), key(items[j])
			if left != right {
				return left < right
			}
		}
		return false
	})
}

// TopRuntimeSymbols sorts items in place by descending count and retains the
// first five. lessTie defines the caller's name order for equal counts. The
// returned slice shares items' backing array and preserves nil versus empty.
func TopRuntimeSymbols(items []RuntimeSymbolUsage, lessTie func(left, right RuntimeSymbolUsage) bool) []RuntimeSymbolUsage {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return lessTie(items[i], items[j])
	})
	return items[:min(len(items), 5)]
}
