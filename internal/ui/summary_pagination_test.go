package ui

import (
	"math"
	"reflect"
	"testing"
)

func TestSummaryPageCountAvoidsIntegerOverflow(t *testing.T) {
	tests := []struct {
		name     string
		total    int
		pageSize int
		want     int
	}{
		{name: "empty", total: 0, pageSize: 10, want: 1},
		{name: "negative total", total: -1, pageSize: 10, want: 1},
		{name: "zero size", total: 10, pageSize: 0, want: 1},
		{name: "negative size", total: 10, pageSize: -1, want: 1},
		{name: "single page", total: 1, pageSize: 10, want: 1},
		{name: "exact multiple", total: 20, pageSize: 10, want: 2},
		{name: "partial last page", total: 21, pageSize: 10, want: 3},
		{name: "maximum size small total", total: 2, pageSize: math.MaxInt, want: 1},
		{name: "maximum total and size", total: math.MaxInt, pageSize: math.MaxInt, want: 1},
		{name: "maximum total unit size", total: math.MaxInt, pageSize: 1, want: math.MaxInt},
		{name: "maximum total two pages", total: math.MaxInt, pageSize: math.MaxInt - 1, want: 2},
		{name: "maximum total half size", total: math.MaxInt, pageSize: math.MaxInt / 2, want: 3},
		{name: "maximum total rounded half", total: math.MaxInt, pageSize: math.MaxInt/2 + 1, want: 2},
		{name: "maximum total size two", total: math.MaxInt, pageSize: 2, want: math.MaxInt/2 + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pageCount(tt.total, tt.pageSize); got != tt.want {
				t.Fatalf("pageCount(%d, %d) = %d, want %d", tt.total, tt.pageSize, got, tt.want)
			}
		})
	}
}

func TestSummaryPaginationBoundsBeforeArithmetic(t *testing.T) {
	deps := []summaryDependencyView{{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"}}
	tests := []struct {
		name     string
		page     int
		pageSize int
		want     []summaryDependencyView
	}{
		{name: "first page", page: 1, pageSize: 2, want: deps[:2]},
		{name: "last partial page", page: 2, pageSize: 2, want: deps[2:]},
		{name: "last full page", page: 3, pageSize: 1, want: deps[2:]},
		{name: "normalize low page", page: math.MinInt, pageSize: 2, want: deps[:2]},
		{name: "maximum size", page: 1, pageSize: math.MaxInt, want: deps},
		{name: "past last page", page: 3, pageSize: 2},
		{name: "maximum page", page: math.MaxInt, pageSize: 2},
		{name: "maximum page and size", page: math.MaxInt, pageSize: math.MaxInt},
		{name: "zero size", page: math.MaxInt, pageSize: 0, want: deps},
		{name: "negative size", page: math.MaxInt, pageSize: -1, want: deps},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := paginateDependencies(deps, tt.page, tt.pageSize); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("paginateDependencies(page=%d, size=%d) = %#v, want %#v", tt.page, tt.pageSize, got, tt.want)
			}
		})
	}
	if got := paginateDependencies(nil, math.MaxInt, math.MaxInt); !reflect.DeepEqual(got, []summaryDependencyView(nil)) {
		t.Fatalf("empty dependency page = %#v, want nil", got)
	}
}

func TestSummaryPaginationNormalizesPagesWithMaximumSize(t *testing.T) {
	deps := []summaryDependencyView{{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"}}
	tests := []struct {
		name     string
		deps     []summaryDependencyView
		page     int
		pageSize int
		filter   string
		wantPage int
		wantLast int
		wantDeps []summaryDependencyView
	}{
		{name: "maximum size", deps: deps, page: 1, pageSize: math.MaxInt, wantPage: 1, wantLast: 1, wantDeps: deps},
		{name: "high page", deps: deps, page: math.MaxInt, pageSize: math.MaxInt, wantPage: 1, wantLast: 1, wantDeps: deps},
		{name: "low page", deps: deps, page: math.MinInt, pageSize: math.MaxInt, wantPage: 1, wantLast: 1, wantDeps: deps},
		{name: "empty", page: math.MaxInt, pageSize: math.MaxInt, wantPage: 1, wantLast: 1},
		{name: "single dependency", deps: deps[:1], page: math.MaxInt, pageSize: math.MaxInt, wantPage: 1, wantLast: 1, wantDeps: deps[:1]},
		{name: "multiple pages", deps: deps, page: math.MaxInt, pageSize: 2, wantPage: 2, wantLast: 2, wantDeps: deps[2:]},
		{name: "filtered", deps: deps, page: math.MaxInt, pageSize: math.MaxInt, filter: "alpha", wantPage: 1, wantLast: 1, wantDeps: deps[:1]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reportView := summaryReportView{Dependencies: tt.deps}
			state := summaryState{page: tt.page, pageSize: tt.pageSize, filter: tt.filter, sortMode: sortByName}
			_, paged, normalized, totalPages := runSummaryDependencyPipeline(reportView, state)
			if normalized.page != tt.wantPage || totalPages != tt.wantLast || !reflect.DeepEqual(paged, tt.wantDeps) {
				t.Fatalf("pipeline page=%d/%d deps=%#v, want page=%d/%d deps=%#v", normalized.page, totalPages, paged, tt.wantPage, tt.wantLast, tt.wantDeps)
			}
			clampSummaryPage(reportView, &state)
			if state.page != tt.wantPage {
				t.Fatalf("clamped page=%d, want %d", state.page, tt.wantPage)
			}
		})
	}
}
