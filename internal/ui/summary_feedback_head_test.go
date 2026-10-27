package ui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSummaryFeedbackFramePreservesWriteErrors(t *testing.T) {
	writeErr := errors.New("feedback output failed")
	for _, failure := range []struct {
		name string
		at   int
	}{{"clear", 0}, {"summary", 1}, {"feedback", 2}} {
		t.Run(failure.name, func(t *testing.T) {
			out := &failAfterWriter{failAt: failure.at, err: writeErr}
			summary := NewSummary(out, strings.NewReader(""), &stubAnalyzer{}, nil)
			err := summary.renderSummaryFrame(summaryReportView{}, &summaryState{page: 1, pageSize: 10}, true, "feedback")
			if !errors.Is(err, writeErr) {
				t.Fatalf("write error lost: %v", err)
			}
		})
	}
}

func TestSummaryFeedbackPreservesInputCancellation(t *testing.T) {
	inputErr := &staveInputError{err: context.Canceled}
	input := &errReader{err: inputErr}
	output := &strings.Builder{}
	summary := NewSummary(output, input, &stubAnalyzer{}, nil)
	err := summary.Start(context.Background(), Options{})
	if !reflect.ValueOf(err).Equal(reflect.ValueOf(inputErr)) || !errors.Is(err, context.Canceled) {
		t.Fatalf("input cancellation identity lost: %v", err)
	}
	if summary.In != input || summary.Out != output {
		t.Fatal("input error changed stream ownership")
	}
}
