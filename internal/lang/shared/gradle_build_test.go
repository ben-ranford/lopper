package shared

import (
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

func TestParseGradleDependencyCoordinatesForGroovyAndKotlin(t *testing.T) {
	groovyDeps := ParseGradleDependencyCoordinatesForFile("build.gradle", `
dependencies {
  implementation 'com.squareup.okhttp3:okhttp:4.12.0'
  implementation group: 'com.google.guava', name: 'guava', version: '33.2.0-jre'
  implementation(platform("org.springframework.boot:spring-boot-dependencies:3.3.0"))
}
`)
	assertGradleCoordinate(t, groovyDeps, "com.squareup.okhttp3", "okhttp", "4.12.0")
	assertGradleCoordinate(t, groovyDeps, "com.google.guava", "guava", "33.2.0-jre")
	assertGradleCoordinate(t, groovyDeps, "org.springframework.boot", "spring-boot-dependencies", "3.3.0")

	kotlinDeps := ParseGradleDependencyCoordinatesForFile("build.gradle.kts", `
dependencies {
  implementation("androidx.core:core-ktx:1.13.1")
  implementation(group = "com.squareup.okhttp3", name = "okhttp", version = "4.12.0")
  implementation(enforcedPlatform("org.springframework.boot:spring-boot-dependencies:3.3.0"))
}
`)
	assertGradleCoordinate(t, kotlinDeps, "androidx.core", "core-ktx", "1.13.1")
	assertGradleCoordinate(t, kotlinDeps, "com.squareup.okhttp3", "okhttp", "4.12.0")
	assertGradleCoordinate(t, kotlinDeps, "org.springframework.boot", "spring-boot-dependencies", "3.3.0")

	if got := ParseGradleDependencyCoordinates("", nil); len(got) != 0 {
		t.Fatalf("expected nil parser inputs to produce no coordinates, got %#v", got)
	}
}

func TestParseGradleCatalogReferencesFromDependencyCalls(t *testing.T) {
	references := parseGradleCatalogReferencesForFile("build.gradle.kts", `
dependencies {
  implementation(libs.okhttp)
  implementation(libs.bundles.networking)
  implementation(testLibs.findBundle("qa").get())
  implementation(testLibs.findLibrary("junit").get())
  implementation(libs["retrofit"])
  implementation(libs.plugins.android)
  implementation(libs.versions.kotlin)
  implementation(platform(libs.spring.boot))
}
`)
	assertGradleCatalogReference(t, references, "libs", "okhttp", false, "")
	assertGradleCatalogReference(t, references, "libs", "networking", true, "")
	assertGradleCatalogReference(t, references, "testlibs", "qa", true, "")
	assertGradleCatalogReference(t, references, "testlibs", "junit", false, "")
	assertGradleCatalogReference(t, references, "libs", "retrofit", false, "")
	assertGradleCatalogReference(t, references, "libs", "", false, "libs.plugins.android")
	assertGradleCatalogReference(t, references, "libs", "", false, "libs.versions.kotlin")
	assertGradleCatalogReference(t, references, "libs", "spring.boot", false, "")

	if _, ok := parseGradleCatalogFinderExpression(`libs.findLibrary(foo).get()`); ok {
		t.Fatalf("expected finder expression without quoted alias to be rejected")
	}
	if _, ok := parseGradleCatalogBracketExpression(`libs[foo]`); ok {
		t.Fatalf("expected bracket expression without quoted alias to be rejected")
	}
	if _, ok := parseGradleCatalogPropertyExpression("libs"); ok {
		t.Fatalf("expected single-segment catalog expression to be rejected")
	}
	if got := parseGradleCatalogReferencesForFile("build.gradle", ""); len(got) != 0 {
		t.Fatalf("expected empty Gradle content to produce no catalog references, got %#v", got)
	}
	if _, ok := parseGradleCatalogReferenceExpression(""); ok {
		t.Fatalf("expected empty catalog reference expression to be rejected")
	}
	if _, ok := parseGradleCatalogFinderExpression("libs.findLibrary()"); ok {
		t.Fatalf("expected finder expression without quoted value to be rejected")
	}
	if got := stripGradleExpressionSpaces(" libs . foo \n"); got != "libs.foo" {
		t.Fatalf("unexpected Gradle expression whitespace stripping: %q", got)
	}
	if got, ok := firstGradleQuotedValue("no quote"); ok || got != "" {
		t.Fatalf("expected missing quoted value to be rejected, got %q %t", got, ok)
	}
}

func TestGradleBuildParserDefensiveHelpers(t *testing.T) {
	if args := gradleCallArguments(nil); len(args) != 0 {
		t.Fatalf("expected nil call arguments to return none, got %#v", args)
	}
	if children := gradleNamedChildren(nil); len(children) != 0 {
		t.Fatalf("expected nil named children to return none, got %#v", children)
	}
	if text := gradleNodeText(nil, nil); text != "" {
		t.Fatalf("expected nil node text to be empty, got %q", text)
	}
	if _, ok := gradleCoordinateFromArgument(nil, nil); ok {
		t.Fatalf("expected nil coordinate argument to be rejected")
	}
	if _, ok := parseGradleCoordinate("missing-version-parts"); ok {
		t.Fatalf("expected malformed Gradle coordinate to be rejected")
	}
	if _, ok := parseGradleCoordinate(":artifact:1.0.0"); ok {
		t.Fatalf("expected Gradle coordinate without group to be rejected")
	}
	if coordinate, ok := parseGradleCoordinate("com.example:demo"); !ok || coordinate.Group != "com.example" || coordinate.Artifact != "demo" || coordinate.Version != "" {
		t.Fatalf("expected two-part Gradle coordinate to parse without version, got %#v %t", coordinate, ok)
	}
	visited := false
	walkGradleNode(nil, func(*sitter.Node) { visited = true })
	if visited {
		t.Fatalf("expected nil Gradle node walk to skip visitor")
	}
	if _, ok := gradleStringValue(nil, nil); ok {
		t.Fatalf("expected nil Gradle string value to be rejected")
	}
	if expressions := gradleDependencyArgumentExpressions(nil, nil); len(expressions) != 0 {
		t.Fatalf("expected nil Gradle dependency argument to return no expressions, got %#v", expressions)
	}
	if text, ok := gradleStringLiteralText(nil, nil); ok || text != "" {
		t.Fatalf("expected nil Gradle string literal to be rejected, got %q %t", text, ok)
	}
	fields := map[string]string{}
	collectGradleNamedArgument(fields, nil, nil)
	if len(fields) != 0 {
		t.Fatalf("expected nil named Gradle argument to leave fields empty, got %#v", fields)
	}
	if _, ok := gradleCoordinateFromFields(map[string]string{"group": "com.example"}); ok {
		t.Fatalf("expected incomplete Gradle coordinate fields to be rejected")
	}
	if _, ok := parseGradleCatalogBracketExpression(`["okhttp"]`); ok {
		t.Fatalf("expected bracket catalog expression without catalog name to be rejected")
	}
	if _, ok := parseGradleCatalogPropertyExpression("libs.findLibrary.foo"); ok {
		t.Fatalf("expected malformed finder property expression to be rejected")
	}
	if _, ok := firstGradleQuotedValue(`"unterminated`); ok {
		t.Fatalf("expected unterminated quoted value to be rejected")
	}
}

func assertGradleCoordinate(t *testing.T, coordinates []GradleDependencyCoordinate, group, artifact, version string) {
	t.Helper()
	for _, coordinate := range coordinates {
		if coordinate.Group == group && coordinate.Artifact == artifact && coordinate.Version == version {
			return
		}
	}
	t.Fatalf("expected Gradle coordinate %s:%s:%s in %#v", group, artifact, version, coordinates)
}

func assertGradleCatalogReference(t *testing.T, references []gradleCatalogReference, catalogName, alias string, bundle bool, unsupported string) {
	t.Helper()
	for _, reference := range references {
		if reference.catalogName == catalogName && reference.alias == alias && reference.bundle == bundle && reference.unsupportedExpression == unsupported {
			return
		}
	}
	t.Fatalf("expected Gradle catalog reference catalog=%q alias=%q bundle=%t unsupported=%q in %#v", catalogName, alias, bundle, unsupported, references)
}
