package testutil

import (
	"context"
	"reflect"
	"testing"
)

func AssertExplicitDependencyOverridesTopN[Request any, Result any](t *testing.T, dependencies []string, makeRequest func(string, int) Request, analyse func(context.Context, Request) (Result, error), summarize func(Result) (int, any)) {
	t.Helper()
	for _, dependency := range dependencies {
		t.Run(dependency, func(t *testing.T) {
			expected, err := analyse(context.Background(), makeRequest(dependency, 0))
			if err != nil {
				t.Fatal(err)
			}
			expectedCount, expectedSummary := summarize(expected)
			if expectedCount != 1 {
				t.Fatalf("expected one explicit dependency, got %d", expectedCount)
			}

			actual, err := analyse(context.Background(), makeRequest(dependency, 2))
			if err != nil {
				t.Fatal(err)
			}
			actualCount, actualSummary := summarize(actual)
			if actualCount != 1 || !reflect.DeepEqual(actualSummary, expectedSummary) {
				t.Fatalf("TopN changed explicit dependency report: got %#v, want %#v", actualSummary, expectedSummary)
			}
		})
	}
}
