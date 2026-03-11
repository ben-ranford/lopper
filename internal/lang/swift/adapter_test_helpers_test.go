package swift

import (
	"context"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

const (
	alamofireRepositoryURL      = "https://github.com/Alamofire/Alamofire.git"
	swiftNIORepositoryURL       = "https://github.com/apple/swift-nio.git"
	swiftBuildDirName           = ".build"
	swiftMainFileName           = "main.swift"
	swiftNIOID                  = "swift-nio"
	expectedOneDependencyReport = "expected one dependency report, got %d"
	analyseErrorFormat          = "analyse: %v"
)

func alamofireFixtureDependency() swiftFixtureDependency {
	return swiftFixtureDependency{
		identity:    "alamofire",
		url:         alamofireRepositoryURL,
		version:     "5.8.0",
		productName: "Alamofire",
	}
}

func swiftNIOFixtureDependency() swiftFixtureDependency {
	return swiftFixtureDependency{
		identity:     swiftNIOID,
		manifestName: swiftNIOID,
		url:          swiftNIORepositoryURL,
		version:      "2.60.0",
		productName:  "NIO",
	}
}

func mustAnalyseSwiftRequest(t *testing.T, req language.Request) report.Report {
	t.Helper()

	reportData, err := NewAdapter().Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf(analyseErrorFormat, err)
	}
	return reportData
}

func mustSingleSwiftDependencyReport(t *testing.T, req language.Request) report.DependencyReport {
	t.Helper()

	reportData := mustAnalyseSwiftRequest(t, req)
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyReport, len(reportData.Dependencies))
	}
	return reportData.Dependencies[0]
}
