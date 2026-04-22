package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/notify"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestExecuteAnalyseDispatchesNotifications(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath:      ".",
			Dependencies:  []report.DependencyReport{{Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50}},
			Warnings:      []string{"existing warning"},
			GeneratedAt:   time.Now().UTC(),
			SchemaVersion: report.SchemaVersion,
		},
	}
	slackNotifier := &fakeNotifier{err: errors.New("slack unreachable")}
	application := &App{
		Analyzer:  analyzer,
		Formatter: report.NewFormatter(),
		Notify: notify.NewDispatcher(map[notify.Channel]notify.Notifier{
			notify.ChannelSlack: slackNotifier,
		}),
	}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.Notifications = notify.Config{
		Slack: notify.ChannelConfig{
			WebhookURL: "https://hooks.slack.com/services/A/B/SECRET",
			Trigger:    notify.TriggerAlways,
		},
	}

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf(executeAnalyseErrFmt, err)
	}
	if !slackNotifier.called {
		t.Fatalf("expected notification dispatch call")
	}
	if slackNotifier.lastDelivery.Channel != notify.ChannelSlack {
		t.Fatalf("expected slack delivery channel, got %q", slackNotifier.lastDelivery.Channel)
	}
	assertContainsAll(t, output, []string{
		"existing warning",
		"notification delivery failed for slack",
		"https://hooks.slack.com",
	})
	if strings.Contains(output, "SECRET") {
		t.Fatalf("expected redacted webhook warning, got %q", output)
	}
}

func TestPrepareRuntimeTraceWithRuntimeCommand(t *testing.T) {
	repo := t.TempDir()
	req := DefaultRequest()
	req.RepoPath = repo
	req.Analyse.RuntimeTestCommand = "make -v"

	warnings, tracePath := prepareRuntimeTrace(context.Background(), req)
	if len(warnings) != 0 {
		t.Fatalf("did not expect warnings from runtime planning: %#v", warnings)
	}
	if tracePath != "" {
		t.Fatalf("expected runtime planning to defer default trace path resolution, got %q", tracePath)
	}
}

const missingRuntimeMakeTarget = "make __missing_target__"

func TestPrepareRuntimeTraceKeepsExplicitTracePath(t *testing.T) {
	repo := t.TempDir()
	explicitPath := filepath.Join(repo, ".artifacts", "explicit.ndjson")
	req := DefaultRequest()
	req.RepoPath = repo
	req.Analyse.RuntimeTracePath = explicitPath
	req.Analyse.RuntimeTestCommand = missingRuntimeMakeTarget

	warnings, tracePath := prepareRuntimeTrace(context.Background(), req)
	if len(warnings) != 0 {
		t.Fatalf("expected runtime planning with explicit path to avoid warnings, got %#v", warnings)
	}
	if tracePath != req.Analyse.RuntimeTracePath {
		t.Fatalf("expected explicit trace path to be retained, got %q", tracePath)
	}
}

func TestPrepareRuntimeTraceMissingWorkingDirectoryStillReturnsConfiguredPath(t *testing.T) {
	orphanedCWD := t.TempDir()
	t.Chdir(orphanedCWD)
	if err := os.RemoveAll(orphanedCWD); err != nil {
		t.Fatalf("remove orphaned cwd: %v", err)
	}

	explicitPath := filepath.Join(t.TempDir(), testRuntimeTracePath)
	req := DefaultRequest()
	req.RepoPath = "."
	req.Analyse.RuntimeTracePath = explicitPath
	req.Analyse.RuntimeTestCommand = missingRuntimeMakeTarget

	warnings, tracePath := prepareRuntimeTrace(context.Background(), req)
	if len(warnings) != 0 {
		t.Fatalf("expected runtime planning to avoid capture warnings, got %#v", warnings)
	}
	if tracePath != explicitPath {
		t.Fatalf("expected explicit trace path to be retained, got %q", tracePath)
	}
}

func TestAppendNotificationWarningsNilReportData(t *testing.T) {
	application := &App{Notify: notify.NewDefaultDispatcher()}
	cfg := notify.Config{
		Slack: notify.ChannelConfig{
			WebhookURL: "https://hooks.slack.com/services/A/B/SECRET",
			Trigger:    notify.TriggerAlways,
		},
	}
	application.appendNotificationWarnings(context.Background(), cfg, nil, notify.Outcome{})
}

func TestPrepareRuntimeTraceWithoutCommandUsesProvidedTracePath(t *testing.T) {
	req := DefaultRequest()
	req.Analyse.RuntimeTracePath = testRuntimeTracePath
	warnings, tracePath := prepareRuntimeTrace(context.Background(), req)
	if len(warnings) != 0 {
		t.Fatalf("did not expect warnings without runtime command, got %#v", warnings)
	}
	if tracePath != testRuntimeTracePath {
		t.Fatalf("expected provided trace path without capture command, got %q", tracePath)
	}
}

func TestExecuteAnalyseForwardsRuntimeCaptureInputs(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath: ".",
			Dependencies: []report.DependencyReport{
				{Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = t.TempDir()
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.RuntimeTracePath = filepath.Join(req.RepoPath, ".artifacts", "explicit.ndjson")
	req.Analyse.RuntimeTestCommand = missingRuntimeMakeTarget

	_, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute analyse forwarding runtime inputs: %v", err)
	}
	if analyzer.lastReq.RuntimeTestCommand != req.Analyse.RuntimeTestCommand {
		t.Fatalf("expected runtime test command to be forwarded, got %q", analyzer.lastReq.RuntimeTestCommand)
	}
	if analyzer.lastReq.RuntimeTracePath != req.Analyse.RuntimeTracePath {
		t.Fatalf("expected runtime trace path to be forwarded, got %q", analyzer.lastReq.RuntimeTracePath)
	}
	if !analyzer.lastReq.RuntimeTracePathExplicit {
		t.Fatalf("expected explicit runtime trace path marker to be forwarded")
	}
}
