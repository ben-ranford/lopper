package analysis

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestServiceAnnotatesDotNetNuGetIdentity(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "App.csproj"), `<Project><ItemGroup>
  <PackageReference Include="Newtonsoft.Json" />
</ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, dotnetLockFileName), `{
	  "dependencies":{"net8.0":{"Newtonsoft.Json":{"type":"Direct","resolved":"013.00.003.0+build"}}}
}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Program.cs"), "using Newtonsoft.Json;\n")

	reportData, err := NewService().Analyse(context.Background(), Request{
		RepoPath:   repoPath,
		Language:   "dotnet",
		Dependency: "newtonsoft.json",
		Features:   mustResolveDependencyIdentityPreviewFeatureSet(t),
	})
	if err != nil {
		t.Fatalf("analyse dotnet identity: %v", err)
	}
	assertIdentity(t, findIdentityDependency(t, reportData, "dotnet", "newtonsoft.json"), report.DependencyIdentity{
		Ecosystem: "nuget", Name: "newtonsoft.json", Version: "13.0.3", VersionStatus: identityStatusResolved,
		PURL: "pkg:nuget/newtonsoft.json@13.0.3", PURLStatus: identityStatusResolved, Source: dotnetLockFileName, Confidence: "high",
	})
}

func TestServiceAnnotatesDotNetNuGetIdentityInWhitespaceNamedDirectory(t *testing.T) {
	repoPath := t.TempDir()
	projectDir := filepath.Join(repoPath, " bin ")
	testutil.MustWriteFile(t, filepath.Join(projectDir, "App.csproj"), `<Project><ItemGroup>
  <PackageReference Include="Space.Package" Version="[1.2.3]" />
</ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(projectDir, "Program.cs"), "using Space.Package;\n")

	reportData, err := NewService().Analyse(context.Background(), Request{
		RepoPath:   repoPath,
		Language:   "dotnet",
		Dependency: "space.package",
		Features:   mustResolveDependencyIdentityPreviewFeatureSet(t),
	})
	if err != nil {
		t.Fatalf("analyse dotnet identity: %v", err)
	}
	assertIdentity(t, findIdentityDependency(t, reportData, "dotnet", "space.package"), report.DependencyIdentity{
		Ecosystem: "nuget", Name: "space.package", Version: "1.2.3", VersionStatus: identityStatusDeclared,
		PURL: "pkg:nuget/space.package@1.2.3", PURLStatus: identityStatusResolved, Source: "bin /App.csproj", Confidence: "high",
	})
}

func TestAnnotateDependencyIdentitiesSkipsDotNetDiscoveryForOtherLanguages(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "target", "Bad.csproj"), `<Project>`)
	reportData := report.Report{Dependencies: []report.DependencyReport{{Language: "python", Name: "requests"}}}

	annotateDependencyIdentities(repoPath, &reportData)

	if len(reportData.Warnings) != 0 {
		t.Fatalf("expected no unrelated .NET warnings, got %#v", reportData.Warnings)
	}
}

func TestAnnotateDependencyIdentitiesUsesNuGetDirectLockEvidence(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "App.csproj"), `<Project>
  <ItemGroup>
	    <PackageReference Include="Newtonsoft.Json" Version="[13.0.3]" />
    <PackageReference Include="Transitive.Package" />
  </ItemGroup>
</Project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, dotnetLockFileName), `{
  "version": 1,
  "dependencies": {
    "net8.0": {
	      "NEWTONSOFT.JSON": {"type": "Direct", "requested": "[13.0.3]", "resolved": "13.0.3"},
      "Transitive.Package": {"type": "Transitive", "resolved": "4.0.0"},
      "Project.Only": {"type": "Project", "resolved": "1.0.0"}
    }
  }
}`)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "dotnet", Name: "newtonsoft.json"},
		{Language: "dotnet", Name: "transitive.package"},
		{Language: "dotnet", Name: "project.only"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertIdentity(t, findIdentityDependency(t, reportData, "dotnet", "newtonsoft.json"), report.DependencyIdentity{
		Ecosystem: "nuget", Name: "newtonsoft.json", Version: "13.0.3", VersionStatus: identityStatusResolved,
		PURL: "pkg:nuget/newtonsoft.json@13.0.3", PURLStatus: identityStatusResolved, Source: dotnetLockFileName, Confidence: "high",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "dotnet", "transitive.package"), report.DependencyIdentity{
		Ecosystem: "nuget", Name: "transitive.package", VersionStatus: identityStatusUnknown,
		PURLStatus: identityPURLUnavailable, Source: "App.csproj", Confidence: "high",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "dotnet", "project.only"), report.DependencyIdentity{
		Ecosystem: "nuget", Name: "project.only", VersionStatus: identityStatusUnknown,
		PURLStatus: identityPURLUnavailable, Source: "language-adapter", Confidence: "low",
	})
}

func TestNuGetIdentityConflictsAcrossTargetFrameworkVersions(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "App.csproj"), `<Project><ItemGroup>
  <PackageReference Include="Multi.Target" />
  <PackageReference Include="Stable.Target" />
</ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, dotnetLockFileName), `{
  "dependencies": {
    "net8.0": {
      "Multi.Target": {"type": "Direct", "resolved": "1.0.0"},
      "Stable.Target": {"type": "Direct", "resolved": "3.0.0"}
    },
    "net9.0": {
      "Multi.Target": {"type": "direct", "resolved": "2.0.0"},
      "Stable.Target": {"type": "Direct", "resolved": "3.0.0"},
      "Blank.Target": {"type": "Direct", "resolved": ""}
    }
  }
}`)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "dotnet", Name: "multi.target"},
		{Language: "dotnet", Name: "stable.target"},
		{Language: "dotnet", Name: "blank.target"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	multi := findIdentityDependency(t, reportData, "dotnet", "multi.target").Identity
	if multi.VersionStatus != identityStatusConflicting || multi.Version != "" || multi.PURLStatus != identityPURLUnavailable {
		t.Fatalf("expected conflicting multi-target identity, got %#v", multi)
	}
	for _, want := range []string{"1.0.0 from packages.lock.json", "2.0.0 from packages.lock.json"} {
		if !strings.Contains(strings.Join(multi.Conflicts, "\n"), want) {
			t.Fatalf("expected conflict %q, got %#v", want, multi.Conflicts)
		}
	}
	assertIdentity(t, findIdentityDependency(t, reportData, "dotnet", "stable.target"), report.DependencyIdentity{
		Ecosystem: "nuget", Name: "stable.target", Version: "3.0.0", VersionStatus: identityStatusResolved,
		PURL: "pkg:nuget/stable.target@3.0.0", PURLStatus: identityStatusResolved, Source: dotnetLockFileName, Confidence: "high",
	})
	assertUnknownNuGetIdentity(t, findIdentityDependency(t, reportData, "dotnet", "blank.target"), "language-adapter", "low")
}

func TestNuGetIdentityUsesExactProjectDeclarations(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "App.csproj"), `<Project xmlns="urn:msbuild">
  <ItemGroup>
    <PackageReference Include="Exact.Attr" Version="[1.0.0]" />
    <PackageReference Include="Exact.Child"><Version>[2.0.0]</Version><PrivateAssets>all</PrivateAssets></PackageReference>
    <PackageReference Include="Override.Package" Version="[1.0.0]"><VersionOverride>[3.0.0]</VersionOverride></PackageReference>
    <PackageReference Include="Updated.Package" Version="[1.0.0]" />
    <PackageReference Update="Updated.Package" Version="[2.0.0]" />
    <PackageReference Include="Override.Updated" Version="[1.0.0]" />
    <PackageReference Update="Override.Updated" VersionOverride="[4.0.0]" />
    <PackageReference Include="Conditional.Package" Version="[1.0.0]" />
    <PackageReference Update="Conditional.Package" Version="[2.0.0]" Condition="'$(TargetFramework)' == 'net9.0'" />
    <PackageReference Include="Plain.Range" Version="4.0.0" />
    <PackageReference Include="Floating.Range" Version="4.*" />
    <PackageReference Include="Interval.Range" Version="[4.0.0,5.0.0)" />
	    <PackageReference Include="Property.Range" Version="$(PackageVersion)" />
	    <PackageReference Include="$(PackageId)" Version="[9.0.0]" />
	    <PackageReference Include="@(Packages)" Version="[9.0.0]" />
	    <PackageReference Include="%(Identity)" Version="[9.0.0]" />
	    <PackageReference Update="Missing.Package" Version="[8.0.0]" />
	  </ItemGroup>
	  <ItemGroup Condition="'$(TargetFramework)' == 'net9.0'">
	    <PackageReference Update="Conditional.Package" Version="[3.0.0]" />
	  </ItemGroup>
	  <ItemGroup>
	    <PackageReference Include="Metadata.Conditional" Version="[1.0.0]">
	      <Version Condition="'$(TargetFramework)' == 'net9.0'">[2.0.0]</Version>
	    </PackageReference>
	  </ItemGroup>
	  <Choose>
	    <When Condition="'$(TargetFramework)' == 'net8.0'">
	      <ItemGroup><PackageReference Include="Choose.Package" Version="[1.0.0]" /></ItemGroup>
	    </When>
	    <Otherwise>
	      <ItemGroup><PackageReference Include="Choose.Package" Version="[2.0.0]" /></ItemGroup>
	    </Otherwise>
	  </Choose>
	</Project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Lib.fsproj"), `<Project><ItemGroup>
  <PackageReference Include="FSharp.Exact"><Version>[8.0.0]</Version></PackageReference>
</ItemGroup></Project>`)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "dotnet", Name: "exact.attr"},
		{Language: "dotnet", Name: "exact.child"},
		{Language: "dotnet", Name: "override.package"},
		{Language: "dotnet", Name: "updated.package"},
		{Language: "dotnet", Name: "override.updated"},
		{Language: "dotnet", Name: "conditional.package"},
		{Language: "dotnet", Name: "plain.range"},
		{Language: "dotnet", Name: "floating.range"},
		{Language: "dotnet", Name: "interval.range"},
		{Language: "dotnet", Name: "property.range"},
		{Language: "dotnet", Name: "metadata.conditional"},
		{Language: "dotnet", Name: "choose.package"},
		{Language: "dotnet", Name: "fsharp.exact"},
		{Language: "dotnet", Name: "$(packageid)"},
		{Language: "dotnet", Name: "@(packages)"},
		{Language: "dotnet", Name: "%(identity)"},
		{Language: "dotnet", Name: "missing.package"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertDeclaredNuGetIdentity(t, reportData, "exact.attr", "1.0.0", "App.csproj")
	assertDeclaredNuGetIdentity(t, reportData, "exact.child", "2.0.0", "App.csproj")
	assertDeclaredNuGetIdentity(t, reportData, "override.package", "3.0.0", "App.csproj")
	assertDeclaredNuGetIdentity(t, reportData, "updated.package", "2.0.0", "App.csproj")
	assertDeclaredNuGetIdentity(t, reportData, "override.updated", "4.0.0", "App.csproj")
	assertDeclaredNuGetIdentity(t, reportData, "fsharp.exact", "8.0.0", "Lib.fsproj")

	for _, name := range []string{"conditional.package", "metadata.conditional", "choose.package"} {
		conditional := findIdentityDependency(t, reportData, "dotnet", name).Identity
		if conditional.VersionStatus != identityStatusConflicting || len(conditional.Conflicts) < 2 {
			t.Fatalf("expected %s versions to conflict, got %#v", name, conditional)
		}
	}
	for _, name := range []string{"plain.range", "floating.range", "interval.range", "property.range"} {
		assertUnknownNuGetIdentity(t, findIdentityDependency(t, reportData, "dotnet", name), "App.csproj", "high")
	}
	for _, name := range []string{"$(packageid)", "@(packages)", "%(identity)", "missing.package"} {
		assertUnknownNuGetIdentity(t, findIdentityDependency(t, reportData, "dotnet", name), "language-adapter", "low")
	}
}

func TestNuGetIdentityUsesNearestCentralVersionAndVersionOverride(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, dotnetCentralFileName), `<Project>
  <PropertyGroup><ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally></PropertyGroup>
  <ItemGroup>
  <PackageVersion Include="Root.Package" Version="[1.1.0]" />
  <PackageVersion Include="Central.Range" Version="4.0.0" />
  <PackageVersion Include="Nested.Package" Version="[1.0.0]" />
  <PackageVersion Include="Central.Updated" Version="[1.0.0]" />
  <PackageVersion Update="Central.Updated" Version="[2.0.0]" />
  <PackageVersion Include="Unused.Package" Version="[9.0.0]" />
</ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Root.csproj"), `<Project><ItemGroup>
  <PackageReference Include="Root.Package" />
  <PackageReference Include="Central.Range" />
  <PackageReference Include="Central.Updated" />
</ItemGroup></Project>`)
	nested := filepath.Join(repoPath, "src", "App")
	testutil.MustWriteFile(t, filepath.Join(nested, dotnetCentralFileName), `<Project>
  <PropertyGroup><ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally></PropertyGroup>
  <ItemGroup>
  <PackageVersion Include="Nested.Package" Version="[1.5.0]" />
  <PackageVersion Update="Nested.Package" Version="[2.0.0]" />
  <PackageVersion Include="Override.Package" Version="[1.0.0]" />
</ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(nested, "App.csproj"), `<Project><ItemGroup>
  <PackageReference Include="Nested.Package" />
  <PackageReference Include="Override.Package" VersionOverride="[3.0.0]" />
</ItemGroup></Project>`)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "dotnet", Name: "root.package"},
		{Language: "dotnet", Name: "central.range"},
		{Language: "dotnet", Name: "nested.package"},
		{Language: "dotnet", Name: "central.updated"},
		{Language: "dotnet", Name: "override.package"},
		{Language: "dotnet", Name: "unused.package"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertDeclaredNuGetIdentity(t, reportData, "root.package", "1.1.0", dotnetCentralFileName)
	assertUnknownNuGetIdentity(t, findIdentityDependency(t, reportData, "dotnet", "central.range"), dotnetCentralFileName, "high")
	assertDeclaredNuGetIdentity(t, reportData, "nested.package", "2.0.0", "src/App/"+dotnetCentralFileName)
	assertDeclaredNuGetIdentity(t, reportData, "central.updated", "2.0.0", dotnetCentralFileName)
	assertDeclaredNuGetIdentity(t, reportData, "override.package", "3.0.0", "src/App/App.csproj")
	assertUnknownNuGetIdentity(t, findIdentityDependency(t, reportData, "dotnet", "unused.package"), "language-adapter", "low")
}

func TestNuGetIdentityStaysWithinRepoAndSkipsGeneratedTrees(t *testing.T) {
	parent := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(parent, dotnetCentralFileName), `<Project><ItemGroup>
  <PackageVersion Include="Outside.Package" Version="[9.0.0]" />
</ItemGroup></Project>`)
	repoPath := filepath.Join(parent, "repo")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "App.csproj"), `<Project><ItemGroup>
  <PackageReference Include="Outside.Package" />
</ItemGroup></Project>`)
	for _, dir := range []string{".idea", ".vscode", "bin", "obj", "dist", "packages", "BUILD", "BIN", "Node_Modules", "VENDOR"} {
		testutil.MustWriteFile(t, filepath.Join(repoPath, dir, "Hidden.csproj"), `<Project><ItemGroup>
  <PackageReference Include="Hidden.Package" Version="[7.0.0]" />
</ItemGroup></Project>`)
		testutil.MustWriteFile(t, filepath.Join(repoPath, dir, dotnetLockFileName), `{"dependencies":{"net8.0":{"Hidden.Package":{"type":"Direct","resolved":"8.0.0"}}}}`)
	}
	testutil.MustWriteFile(t, filepath.Join(repoPath, "target", "App.csproj"), `<Project><ItemGroup>
  <PackageReference Include="Target.Package" Version="[3.0.0]" />
</ItemGroup></Project>`)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "dotnet", Name: "outside.package"},
		{Language: "dotnet", Name: "hidden.package"},
		{Language: "dotnet", Name: "target.package"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertUnknownNuGetIdentity(t, findIdentityDependency(t, reportData, "dotnet", "outside.package"), "App.csproj", "high")
	assertUnknownNuGetIdentity(t, findIdentityDependency(t, reportData, "dotnet", "hidden.package"), "language-adapter", "low")
	assertDeclaredNuGetIdentity(t, reportData, "target.package", "3.0.0", "target/App.csproj")
}

func TestNuGetIdentityWarnsOnMalformedAndUnreadableFiles(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Bad.csproj"), `<Project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Bad.fsproj"), `<Project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, dotnetCentralFileName), `<Project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, dotnetLockFileName), `{`)
	reportData := report.Report{Dependencies: []report.DependencyReport{{Language: "dotnet", Name: "missing"}}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertWarningsExact(t, repoPath, reportData.Warnings, []string{
		"identity manifest parse failed for Bad.csproj: invalid XML",
		"identity manifest parse failed for Bad.fsproj: invalid XML",
		"identity manifest parse failed for Directory.Packages.props: invalid XML",
		"identity manifest parse failed for packages.lock.json: invalid JSON",
	})

	warnings := newIdentityWarningCollector(repoPath)
	missing := filepath.Join(t.TempDir(), "missing")
	snapshot := identityManifestSnapshot{
		dotnetProjectFiles: []string{missing + ".csproj"},
		dotnetCentralFiles: []string{missing + ".props"},
		dotnetLockFiles:    []string{missing + ".json"},
	}
	collectDotNetIdentityEvidenceFromSnapshot(repoPath, identityIndex{}, snapshot, warnings)
	if len(warnings.list()) != 3 {
		t.Fatalf("expected three read warnings, got %#v", warnings.list())
	}
}

func TestNuGetIdentityHelpersRejectRangesAndGeneratedPaths(t *testing.T) {
	for _, value := range []string{"1.0.0", "[1.0.0,2.0.0)", "[*]", "[$(Version)]", "[]", "(1.0.0)", "[foo]", "[1.2.3.4.5]", "[1..2]", "[1.0+bad!]", "[2147483648.0.0]", "[1.0.0-01]"} {
		if version, ok := exactNuGetVersion(value); ok || version != "" {
			t.Fatalf("expected %q not to be exact, got %q", value, version)
		}
	}
	validVersions := []struct {
		input string
		want  string
	}{
		{input: "[1]", want: "1.0.0"},
		{input: "[01.00.000.0]", want: "1.0.0"},
		{input: "[1.00.0.1]", want: "1.0.0.1"},
		{input: "[2147483647.0.0]", want: "2147483647.0.0"},
		{input: "[1.0.0-0]", want: "1.0.0-0"},
		{input: "[1.0.0-alpha01]", want: "1.0.0-alpha01"},
		{input: " [1.0.0-beta.1+build] ", want: "1.0.0-beta.1"},
	}
	for _, test := range validVersions {
		if version, ok := exactNuGetVersion(test.input); !ok || version != test.want {
			t.Fatalf("unexpected exact NuGet version for %q: %q, %v", test.input, version, ok)
		}
	}
	for _, path := range []string{"obj/App.csproj", "src/BIN/App.csproj", "Packages/App/App.csproj", "src/BUILD/App.csproj", "VENDOR/App.csproj"} {
		if shouldIncludeDotNetIdentityManifest(path) {
			t.Fatalf("expected %q to be ignored", path)
		}
	}
	if !shouldIncludeDotNetIdentityManifest("src/App/App.csproj") {
		t.Fatal("expected ordinary project manifest to be included")
	}
}

func TestParseNuGetMSBuildItemsRejectsMalformedMetadata(t *testing.T) {
	fixtures := []string{
		`<Project><PackageReference Include="A">`,
		`<Project><PackageReference Include="A"><Version>`,
		`<Project><PackageReference Include="A"><VersionOverride>`,
		`<Project><PackageReference Include="A"><PrivateAssets>`,
	}
	for _, fixture := range fixtures {
		if _, err := parseNuGetMSBuildItems([]byte(fixture), "PackageReference"); err == nil {
			t.Fatalf("expected malformed metadata to fail: %s", fixture)
		}
	}
}

func TestNuGetModelHelpersIgnoreIncompleteUpdates(t *testing.T) {
	project := nugetProjectModel{}
	applyNuGetProjectItem(project, nugetMSBuildItem{})
	applyNuGetProjectItem(project, nugetMSBuildItem{name: "Missing", update: true, version: "[1.0.0]"})
	applyNuGetProjectItem(project, nugetMSBuildItem{name: "Missing", update: true})
	if len(project) != 0 {
		t.Fatalf("expected incomplete project updates to be ignored, got %#v", project)
	}

	central := nugetCentralModel{}
	applyNuGetCentralItem(central, nugetMSBuildItem{}, "Directory.Packages.props")
	applyNuGetCentralItem(central, nugetMSBuildItem{name: "Missing", update: true, version: "[1.0.0]"}, "Directory.Packages.props")
	if len(central) != 0 {
		t.Fatalf("expected incomplete central items to be ignored, got %#v", central)
	}
}

func TestNuGetIdentityConflictsWhenLockDisagreesWithExactDeclaration(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "App.csproj"), `<Project><ItemGroup>
  <PackageReference Include="Stale.Package" Version="[1.0.0]" />
</ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, dotnetLockFileName), `{
  "dependencies":{"net8.0":{"Stale.Package":{"type":"Direct","resolved":"2.0.0"}}}
}`)
	reportData := report.Report{Dependencies: []report.DependencyReport{{Language: "dotnet", Name: "stale.package"}}}

	annotateDependencyIdentities(repoPath, &reportData)

	identity := findIdentityDependency(t, reportData, "dotnet", "stale.package").Identity
	if identity.VersionStatus != identityStatusConflicting || identity.PURLStatus != identityPURLUnavailable {
		t.Fatalf("expected stale lock evidence to conflict, got %#v", identity)
	}
	for _, want := range []string{"1.0.0 from App.csproj", "2.0.0 from packages.lock.json"} {
		if !strings.Contains(strings.Join(identity.Conflicts, "\n"), want) {
			t.Fatalf("expected conflict %q, got %#v", want, identity.Conflicts)
		}
	}
}

func TestNuGetIdentityIgnoresUnprovenProjectSpecificLocks(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "App.csproj"), `<Project><ItemGroup>
  <PackageReference Include="App.Package" />
</ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Lib.fsproj"), `<Project><ItemGroup>
  <PackageReference Include="Lib.Package" />
</ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "packages.App.lock.json"), `{
  "dependencies":{"net8.0":{
    "App.Package":{"type":"Direct","resolved":"01.00.003.0+build"},
    "Stale.Package":{"type":"Direct","resolved":"9.0.0"}
  }}
}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "packages.Lib.lock.json"), `{
  "dependencies":{"net8.0":{"Lib.Package":{"type":"Direct","resolved":"2.0.0"}}}
}`)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "dotnet", Name: "app.package"},
		{Language: "dotnet", Name: "lib.package"},
		{Language: "dotnet", Name: "stale.package"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertUnknownNuGetIdentity(t, findIdentityDependency(t, reportData, "dotnet", "app.package"), "App.csproj", "high")
	assertUnknownNuGetIdentity(t, findIdentityDependency(t, reportData, "dotnet", "lib.package"), "Lib.fsproj", "high")
	assertUnknownNuGetIdentity(t, findIdentityDependency(t, reportData, "dotnet", "stale.package"), "language-adapter", "low")
}

func TestNuGetIdentityRejectsAmbiguousGenericLockOwnership(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "App.csproj"), `<Project><ItemGroup>
  <PackageReference Include="App.Package" />
</ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Lib.fsproj"), `<Project><ItemGroup>
  <PackageReference Include="Lib.Package" />
</ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, dotnetLockFileName), `{
  "dependencies":{"net8.0":{
    "App.Package":{"type":"Direct","resolved":"1.0.0"},
    "Lib.Package":{"type":"Direct","resolved":"2.0.0"}
  }}
}`)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "dotnet", Name: "app.package"},
		{Language: "dotnet", Name: "lib.package"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertUnknownNuGetIdentity(t, findIdentityDependency(t, reportData, "dotnet", "app.package"), "App.csproj", "high")
	assertUnknownNuGetIdentity(t, findIdentityDependency(t, reportData, "dotnet", "lib.package"), "Lib.fsproj", "high")
}

func TestNuGetIdentityRequiresUnconditionalCentralActivation(t *testing.T) {
	tests := map[string]string{
		"missing": `<Project><ItemGroup>
  <PackageVersion Include="Missing.Activation" Version="[1.0.0]" />
</ItemGroup></Project>`,
		"false": `<Project>
  <PropertyGroup><ManagePackageVersionsCentrally>false</ManagePackageVersionsCentrally></PropertyGroup>
  <ItemGroup><PackageVersion Include="False.Activation" Version="[1.0.0]" /></ItemGroup>
</Project>`,
		"conditional": `<Project>
  <PropertyGroup><ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally></PropertyGroup>
  <PropertyGroup Condition="'$(TargetFramework)' == 'net9.0'">
    <ManagePackageVersionsCentrally>false</ManagePackageVersionsCentrally>
  </PropertyGroup>
  <ItemGroup><PackageVersion Include="Conditional.Activation" Version="[1.0.0]" /></ItemGroup>
</Project>`,
	}
	for name, centralContents := range tests {
		t.Run(name, func(t *testing.T) {
			repoPath := t.TempDir()
			packageName := name + ".activation"
			testutil.MustWriteFile(t, filepath.Join(repoPath, dotnetCentralFileName), centralContents)
			testutil.MustWriteFile(t, filepath.Join(repoPath, "App.csproj"), `<Project><ItemGroup>
  <PackageReference Include="`+packageName+`" />
</ItemGroup></Project>`)
			reportData := report.Report{Dependencies: []report.DependencyReport{{Language: "dotnet", Name: packageName}}}

			annotateDependencyIdentities(repoPath, &reportData)

			assertUnknownNuGetIdentity(t, findIdentityDependency(t, reportData, "dotnet", packageName), "App.csproj", "high")
		})
	}
}

func TestNuGetIdentityHonorsProjectCentralActivationOverrides(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, dotnetCentralFileName), `<Project>
  <PropertyGroup><ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally></PropertyGroup>
  <ItemGroup>
    <PackageVersion Include="Disabled.Package" Version="[1.0.0]" />
    <PackageVersion Include="Conditional.Package" Version="[2.0.0]" />
  </ItemGroup>
</Project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Disabled.csproj"), `<Project>
  <PropertyGroup><ManagePackageVersionsCentrally>false</ManagePackageVersionsCentrally></PropertyGroup>
  <ItemGroup><PackageReference Include="Disabled.Package" /></ItemGroup>
</Project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Conditional.csproj"), `<Project>
  <PropertyGroup Condition="'$(TargetFramework)' == 'net9.0'">
    <ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally>
  </PropertyGroup>
  <ItemGroup><PackageReference Include="Conditional.Package" /></ItemGroup>
</Project>`)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "dotnet", Name: "disabled.package"},
		{Language: "dotnet", Name: "conditional.package"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertUnknownNuGetIdentity(t, findIdentityDependency(t, reportData, "dotnet", "disabled.package"), "Disabled.csproj", "high")
	assertUnknownNuGetIdentity(t, findIdentityDependency(t, reportData, "dotnet", "conditional.package"), "Conditional.csproj", "high")
}

func TestNuGetIdentityTreatsConditionalCentralVersionsAsConflicting(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, dotnetCentralFileName), `<Project>
  <PropertyGroup><ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally></PropertyGroup>
  <ItemGroup>
    <PackageVersion Include="Parent.Conditional" Version="[1.0.0]" />
    <PackageVersion Include="Metadata.Conditional" Version="[1.0.0]">
      <Version Condition="'$(TargetFramework)' == 'net9.0'">[2.0.0]</Version>
    </PackageVersion>
    <PackageVersion Update="Standalone.Update" Version="[9.0.0]" />
  </ItemGroup>
  <ItemGroup Condition="'$(TargetFramework)' == 'net9.0'">
    <PackageVersion Update="Parent.Conditional" Version="[2.0.0]" />
  </ItemGroup>
</Project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "App.csproj"), `<Project><ItemGroup>
  <PackageReference Include="Parent.Conditional" />
  <PackageReference Include="Metadata.Conditional" />
  <PackageReference Include="Standalone.Update" />
</ItemGroup></Project>`)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "dotnet", Name: "parent.conditional"},
		{Language: "dotnet", Name: "metadata.conditional"},
		{Language: "dotnet", Name: "standalone.update"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	identity := findIdentityDependency(t, reportData, "dotnet", "parent.conditional").Identity
	if identity.VersionStatus != identityStatusConflicting || len(identity.Conflicts) != 2 {
		t.Fatalf("expected conditional central update to conflict, got %#v", identity)
	}
	assertUnknownNuGetIdentity(t, findIdentityDependency(t, reportData, "dotnet", "metadata.conditional"), dotnetCentralFileName, "high")
	assertUnknownNuGetIdentity(t, findIdentityDependency(t, reportData, "dotnet", "standalone.update"), "App.csproj", "high")
}

func TestNuGetIdentityDoesNotFallThroughMalformedNearestCentralFile(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, dotnetCentralFileName), `<Project>
  <PropertyGroup><ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally></PropertyGroup>
  <ItemGroup><PackageVersion Include="Shadowed.Package" Version="[9.0.0]" /></ItemGroup>
</Project>`)
	nested := filepath.Join(repoPath, "src", "App")
	testutil.MustWriteFile(t, filepath.Join(nested, dotnetCentralFileName), `<Project>`)
	testutil.MustWriteFile(t, filepath.Join(nested, "App.csproj"), `<Project><ItemGroup>
  <PackageReference Include="Shadowed.Package" />
</ItemGroup></Project>`)
	reportData := report.Report{Dependencies: []report.DependencyReport{{Language: "dotnet", Name: "shadowed.package"}}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertUnknownNuGetIdentity(t, findIdentityDependency(t, reportData, "dotnet", "shadowed.package"), "src/App/App.csproj", "high")
	assertWarningsExact(t, repoPath, reportData.Warnings, []string{
		"identity manifest parse failed for src/App/Directory.Packages.props: invalid XML",
	})
}

func TestNuGetConditionalUpdatesKeepUniqueCandidates(t *testing.T) {
	project := nugetProjectModel{}
	applyNuGetProjectItem(project, nugetMSBuildItem{name: "Bounded.Package", version: "[1.0.0]"})
	for range 64 {
		applyNuGetProjectItem(project, nugetMSBuildItem{
			name: "Bounded.Package", version: "[2.0.0]", condition: nugetConditionalMarker, update: true,
		})
	}
	candidates := project["bounded.package"].candidates
	if len(candidates) != 2 {
		t.Fatalf("expected two unique conditional versions, got %#v", candidates)
	}
}

func assertDeclaredNuGetIdentity(t *testing.T, reportData report.Report, name, version, source string) {
	t.Helper()
	assertIdentity(t, findIdentityDependency(t, reportData, "dotnet", name), report.DependencyIdentity{
		Ecosystem: "nuget", Name: name, Version: version, VersionStatus: identityStatusDeclared,
		PURL:       "pkg:nuget/" + name + "@" + strings.ReplaceAll(version, "+", "%2B"),
		PURLStatus: identityStatusResolved, Source: source, Confidence: "high",
	})
}

func assertUnknownNuGetIdentity(t *testing.T, dependency report.DependencyReport, source, confidence string) {
	t.Helper()
	assertIdentity(t, dependency, report.DependencyIdentity{
		Ecosystem: "nuget", Name: dependency.Name, VersionStatus: identityStatusUnknown,
		PURLStatus: identityPURLUnavailable, Source: source, Confidence: confidence,
	})
}
