package dotnet

import (
	"strconv"
	"strings"
	"testing"
)

const (
	benchmarkDotNetImportIterations = 200
	benchmarkProgramSourceName      = "Program.cs"
)

func BenchmarkParseImports(b *testing.B) {
	mapper := newDependencyMapper(benchmarkDeclaredDependencies(256))
	content := benchmarkDotNetImportContent()
	expectedImports := benchmarkDotNetImportIterations * 5

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		imports, meta := parseImports(content, benchmarkProgramSourceName, mapper)
		if len(imports) != expectedImports {
			b.Fatalf("unexpected import count: got %d want %d", len(imports), expectedImports)
		}
		if meta.undeclaredByDependency["unknown.vendor"] == 0 {
			b.Fatalf("expected undeclared import metadata for fallback namespace")
		}
	}
}

func BenchmarkDependencyMapperResolve(b *testing.B) {
	mapper := newDependencyMapper(benchmarkDeclaredDependencies(256))
	modules := []string{
		"Acme.Core.Services",
		"Newtonsoft.Json.Linq",
		"Serilog",
		"Vendor.Pkg42.Component",
		"Unknown.Vendor.Component",
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		dependency, _, _ := mapper.resolve(modules[i%len(modules)])
		if dependency == "" {
			b.Fatal("expected dependency resolution")
		}
	}
}

func benchmarkDeclaredDependencies(total int) []string {
	dependencies := []string{
		"acme.core",
		"newtonsoft.json",
		"serilog.aspnetcore",
	}
	for i := len(dependencies); i < total; i++ {
		dependencies = append(dependencies, "vendor.pkg"+strconv.Itoa(i))
	}
	return dependencies
}

func benchmarkDotNetImportContent() []byte {
	var source strings.Builder
	source.Grow(benchmarkDotNetImportIterations * 160)
	for i := 0; i < benchmarkDotNetImportIterations; i++ {
		source.WriteString("using Acme.Core.Services;\n")
		source.WriteString("using JsonConvert = Newtonsoft.Json.JsonConvert;\n")
		source.WriteString("global using static Serilog.Log;\n")
		source.WriteString("open Acme.Core.Workflows\n")
		source.WriteString("using Unknown.Vendor.Component;\n")
		source.WriteString("using System.Text;\n")
		source.WriteString("var ignored = 1;\n")
		source.WriteString("// comment only\n")
	}
	return []byte(source.String())
}
