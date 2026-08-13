package ui

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

type staveCoverageErrWriter struct{}

func (*staveCoverageErrWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

type staveCoverageErrReader struct{}

func (*staveCoverageErrReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestStaveLineInputDistinguishesCompleteFinalAndReadError(t *testing.T) {
	line, eof, err := readStaveLineInput(bufio.NewReader(strings.NewReader(" refresh \nnext")))
	if err != nil || eof || line != "refresh" {
		t.Fatalf("complete line: %q eof=%v err=%v", line, eof, err)
	}
	line, eof, err = readStaveLineInput(bufio.NewReader(strings.NewReader(" final ")))
	if err != nil || !eof || line != "final" {
		t.Fatalf("unterminated line: %q eof=%v err=%v", line, eof, err)
	}
	_, _, err = readStaveLineInput(bufio.NewReader(&staveCoverageErrReader{}))
	if err == nil || err.Error() != "read failed" {
		t.Fatalf("reader error = %v", err)
	}
}

func TestWriteStaveLineFrameAddsNewlineAndPropagatesWriterErrors(t *testing.T) {
	var out bytes.Buffer
	if err := writeStaveLineFrame(&out, "frame"); err != nil || out.String() != "frame\n" {
		t.Fatalf("newline frame: %q err=%v", out.String(), err)
	}
	out.Reset()
	if err := writeStaveLineFrame(&out, "frame\n"); err != nil || out.String() != "frame\n" {
		t.Fatalf("existing newline: %q err=%v", out.String(), err)
	}
	if err := writeStaveLineFrame(&staveCoverageErrWriter{}, "frame"); err == nil {
		t.Fatal("writer error was swallowed")
	}
}

func TestStavePreviewRenderHonorsCancellationAndReportsAnalyzerErrors(t *testing.T) {
	p := NewStavePreview(NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{err: errors.New("analysis failed")}, nil)).(*StavePreview)
	if _, err := p.render(context.Background(), Options{UseStavePreview: true}); err == nil || !strings.Contains(err.Error(), "analysis failed") {
		t.Fatalf("analyzer error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.renderView(ctx, Options{}, summaryReportView{}, summaryState{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("render cancellation = %v", err)
	}
}

func TestStavePreviewStartLineModeProcessesQuitAndEOF(t *testing.T) {
	data := report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Name: "alpha", Language: "go", UsedPercent: 25, EstimatedUnusedBytes: 8}}}
	for _, input := range []string{"q\n", ""} {
		var out bytes.Buffer
		s := NewSummary(&out, strings.NewReader(input), &stubAnalyzer{report: data}, report.NewFormatter())
		p := NewStavePreview(s).(*StavePreview)
		opts := Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80, PageSize: 10}
		if err := p.Start(context.Background(), opts); err != nil {
			t.Fatalf("line-mode input %q: %v", input, err)
		}
		if !strings.Contains(out.String(), "Stave preview") {
			t.Fatalf("line-mode output missing preview frame for %q: %q", input, out.String())
		}
	}
}

func TestStavePreviewStartPropagatesFrameWriteAndAnalysisErrors(t *testing.T) {
	data := report.Report{SchemaVersion: report.SchemaVersion}
	s := NewSummary(&staveCoverageErrWriter{}, strings.NewReader(""), &stubAnalyzer{report: data}, report.NewFormatter())
	p := NewStavePreview(s).(*StavePreview)
	opts := Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80}
	if err := p.Start(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("frame write error = %v", err)
	}
	s = NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{err: errors.New("analysis failed")}, report.NewFormatter())
	p = NewStavePreview(s).(*StavePreview)
	if err := p.Start(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "analysis failed") {
		t.Fatalf("start analysis error = %v", err)
	}
}

func TestStaveTerminalViewUsesErrorAndQuitFrames(t *testing.T) {
	b := &staveTerminal{err: errors.New("unsafe \x1b[31m error")}
	if got := (&staveTerminalModel{bridge: b}).View().Content; !strings.Contains(got, "unsafe") || strings.Contains(got, "\x1b") {
		t.Fatalf("sanitized error frame = %q", got)
	}
	b.quit = true
	if got := (&staveTerminalModel{bridge: b}).View().Content; got != "" {
		t.Fatalf("quit frame = %q", got)
	}
}

func TestParityKnownGapOracleRejectsUnknownAndDuplicateGaps(t *testing.T) {
	r := ParityReport{CapabilityGaps: []ParityDiff{{Path: "capabilities.interactive"}}}
	if !r.EqualWithKnownGaps(map[string]bool{"capabilities.interactive": true}) {
		t.Fatal("documented gap was rejected")
	}
	unknownGapReport := ParityReport{CapabilityGaps: []ParityDiff{{Path: "unknown"}}}
	if unknownGapReport.EqualWithKnownGaps(map[string]bool{"capabilities.interactive": true}) {
		t.Fatal("unknown gap accepted")
	}
	duplicateGapReport := ParityReport{CapabilityGaps: []ParityDiff{{Path: "capabilities.interactive"}, {Path: "capabilities.interactive"}}}
	if duplicateGapReport.EqualWithKnownGaps(map[string]bool{"capabilities.interactive": true}) {
		t.Fatal("duplicate gap accepted")
	}
}
