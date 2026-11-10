package dotnet

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestFSharpOpenTypeParsesFullNamespace(t *testing.T) {
	for _, input := range []string{
		"open type System.Math",
		"open type Acme.Logging.Log",
		" \topen\ttype\tAcme.Logging.Log \r",
		"open type global.Acme.Logging.Log",
		"open type Acme.Logging.Container<int>.Nested",
		"open type Acme.Logging.Container<string, int>.Nested",
		"open type Acme.Logging.Box<int * string>",
		"open type Acme.Logging.Box<Outer<int * string>, (int -> string)>.Nested",
		"open type Acme.Logging.Box<float<m/s>>",
		"open type Acme.Logging.Box<{| Value: float<m/s>; Name: string |}>",
		"open type Acme.Logging.Log // comment",
	} {
		t.Run(input, func(t *testing.T) {
			want := "Acme.Logging.Log"
			switch input {
			case "open type System.Math":
				want = "System.Math"
			case "open type global.Acme.Logging.Log":
				want = "global.Acme.Logging.Log"
			case "open type Acme.Logging.Container<int>.Nested":
				want = "Acme.Logging.Container<int>.Nested"
			case "open type Acme.Logging.Container<string, int>.Nested":
				want = "Acme.Logging.Container<string, int>.Nested"
			case "open type Acme.Logging.Box<int * string>":
				want = "Acme.Logging.Box<int * string>"
			case "open type Acme.Logging.Box<Outer<int * string>, (int -> string)>.Nested":
				want = "Acme.Logging.Box<Outer<int * string>, (int -> string)>.Nested"
			case "open type Acme.Logging.Box<float<m/s>>":
				want = "Acme.Logging.Box<float<m/s>>"
			case "open type Acme.Logging.Box<{| Value: float<m/s>; Name: string |}>":
				want = "Acme.Logging.Box<{| Value: float<m/s>; Name: string |}>"
			case "open type Acme.Logging.Log // comment":
				want = "Acme.Logging.Log"
			}
			module, ok := parseFSharpOpen(input)
			if !ok || module != want {
				t.Fatalf("parse %q: ok=%t module=%q, want %q", input, ok, module, want)
			}
		})
	}
}

func TestFSharpOpenDirectiveControls(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"open Acme.Logging", "Acme.Logging"},
		{"\topen\tAcme.Logging.Log\r", "Acme.Logging.Log"},
		{"open global.Acme.Logging", "global.Acme.Logging"},
		{"open typewriter.Logging", "typewriter.Logging"},
		{"open type.Logging", "type.Logging"},
		{"opened type Acme.Logging.Log", ""},
		{"open", ""},
		{"open ", ""},
		{"open type", ""},
		{"open type \t", ""},
		{"open type 1Invalid.Namespace", ""},
		{"open type .Acme.Logging", ""},
		{"open type Acme.Logging.Log trailing", ""},
		{"open type Acme.Logging.Container<int>.Nested\ttrailing", ""},
		{"open type Acme.Logging.Container<int", ""},
		{"open type Acme.Logging.Container<int>>", ""},
		{"open type Acme.Logging.Box<(int * string>", ""},
		{"open type Acme.Logging.Box<[int * string>", ""},
		{"open type Acme.Logging.Box<Box<int)>", ""},
		{"open type Acme.Logging.Box<float<m // truncated comment", ""},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			module, ok := parseFSharpOpen(tc.input)
			if module != tc.want || ok != (tc.want != "") {
				t.Fatalf("parse %q: ok=%t module=%q, want %q", tc.input, ok, module, tc.want)
			}
		})
	}
}

func TestFSharpOpenTypeImportAttribution(t *testing.T) {
	content := []byte("// open type Ignored.Type\n\topen\ttype Acme.Logging.Log // comment\r\nopen Acme.Logging\nopen type System.Math\nopen type // missing target\n")
	mapper := newDependencyMapper([]string{"acme.logging"})
	mapper.allowFallback = true
	imports, meta := parseImports(content, "Module.fs", mapper)
	if len(imports) != 2 {
		t.Fatalf("expected only the two Acme imports, got %#v", imports)
	}
	for i, imported := range imports {
		wantModule := "Acme.Logging.Log"
		if i == 1 {
			wantModule = "Acme.Logging"
		}
		if imported.Dependency != "acme.logging" || imported.Module != wantModule || !imported.Wildcard || imported.Name != "*" {
			t.Fatalf("unexpected import attribution: %#v", imported)
		}
		if imported.Location.File != "Module.fs" || imported.Location.Line != i+2 || imported.Location.Column != 2-i {
			t.Fatalf("unexpected import location: %#v", imported.Location)
		}
	}
	if len(meta.ambiguousByDependency) != 0 || len(meta.undeclaredByDependency) != 0 {
		t.Fatalf("unexpected mapping diagnostics: %#v", meta)
	}
	for _, source := range []string{"open type Acme.Logging.Log trailing", "open type Acme.Logging.Container<int>.Nested\ttrailing"} {
		assertQualifiedSourceAttribution(t, source, "")
	}
	assertQualifiedOpenTypeAttribution(t)
	imports, _ = parseImports([]byte("open type Acme.Logging.Container<int>.Nested\nopen type Acme.Logging.Container<string>.Nested\n"), "Types.fs", mapper)
	if len(imports) != 2 || imports[0].Module == imports[1].Module {
		t.Fatalf("expected distinct generic opened types to remain distinct, got %#v", imports)
	}
}

func TestAdapterAnalyseFSharpOpenTypeDependency(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "App.fsproj"), `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup><PackageReference Include="Acme.Logging" Version="1.0.0" /></ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, "Module.fs"), "open type Acme.Logging.Log // static members\n")
	data, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "acme.logging"})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	dep := expectSingleDotNetDependency(t, data.Dependencies)
	if dep.Name != "acme.logging" || len(dep.UsedImports) != 1 || dep.UsedImports[0].Module != "Acme.Logging.Log" {
		t.Fatalf("expected opened type attributed to acme.logging, got %#v", dep)
	}
}

func assertQualifiedOpenTypeAttribution(t *testing.T) {
	cases := []struct {
		name, source, module string
	}{
		{"qualified framework", "open type global.System.Math", ""},
		{"qualified declared package", "open type global.Acme.Logging.Log", "global.Acme.Logging.Log"},
		{"ordinary qualified framework", "open global.System", ""},
		{"ordinary qualified package", "open global.Acme.Logging", "global.Acme.Logging"},
		{"CSharp qualified framework", "using static global::System.Math;", ""},
		{"CSharp qualified package", "using static global::Acme.Logging.Log;", "Acme.Logging.Log"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertQualifiedSourceAttribution(t, tc.source, tc.module)
		})
	}
	for _, tc := range []struct {
		name, source, module string
	}{
		{"generic opened type", "open type Acme.Logging.Container<int>", "Acme.Logging.Container<int>"},
		{"nested generic opened type", "open type Acme.Logging.Container<int>.Nested", "Acme.Logging.Container<int>.Nested"},
		{"multiple generic arguments", "open type Acme.Logging.Container<string, int>.Nested", "Acme.Logging.Container<string, int>.Nested"},
		{"tuple generic argument", "open type Acme.Logging.Box<int * string>", "Acme.Logging.Box<int * string>"},
		{"nested tuple and function arguments", "open type Acme.Logging.Box<Outer<int * string>, (int -> string)>.Nested", "Acme.Logging.Box<Outer<int * string>, (int -> string)>.Nested"},
		{"unit of measure argument", "open type Acme.Logging.Box<float<m/s>>", "Acme.Logging.Box<float<m/s>>"},
		{"anonymous record argument", "open type Acme.Logging.Box<{| Value: float<m/s>; Name: string |}>", "Acme.Logging.Box<{| Value: float<m/s>; Name: string |}>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertQualifiedSourceAttribution(t, tc.source, tc.module)
		})
	}
}

func assertQualifiedSourceAttribution(t *testing.T, source, module string) {
	t.Helper()
	imports, meta := parseImports([]byte("\t"+source+" // comment\r\n"), "Qualified.fs", newDependencyMapper([]string{"acme.logging"}))
	wantCount := 0
	if module != "" {
		wantCount = 1
	}
	if len(imports) != wantCount {
		t.Fatalf("expected %d imports, got %#v", wantCount, imports)
	}
	if len(meta.ambiguousByDependency) != 0 || len(meta.undeclaredByDependency) != 0 {
		t.Fatalf("unexpected qualified mapping diagnostics: %#v", meta)
	}
	if len(imports) == 0 {
		return
	}
	imported := imports[0]
	if imported.Dependency != "acme.logging" || imported.Module != module || !imported.Wildcard || imported.Name != "*" {
		t.Fatalf("unexpected qualified attribution: %#v", imported)
	}
	if imported.Location.File != "Qualified.fs" || imported.Location.Line != 1 || imported.Location.Column != 2 {
		t.Fatalf("unexpected qualified location: %#v", imported.Location)
	}
}
