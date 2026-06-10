package shared

import (
	"context"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/groovy"
	"github.com/smacker/go-tree-sitter/kotlin"
)

type GradleDependencyCoordinate struct {
	Group    string
	Artifact string
	Version  string
}

type gradleCatalogReference struct {
	catalogName           string
	alias                 string
	bundle                bool
	unsupportedExpression string
}

var gradleDependencyConfigurations = map[string]struct{}{
	"androidTestImplementation": {},
	"annotationProcessor":       {},
	"api":                       {},
	"classpath":                 {},
	"compileOnly":               {},
	"debugImplementation":       {},
	"implementation":            {},
	"kapt":                      {},
	"kaptAndroidTest":           {},
	"kaptTest":                  {},
	"ksp":                       {},
	"releaseImplementation":     {},
	"runtimeOnly":               {},
	"testAnnotationProcessor":   {},
	"testCompileOnly":           {},
	"testImplementation":        {},
	"testRuntimeOnly":           {},
}

func ParseGradleDependencyCoordinatesForFile(path, content string) []GradleDependencyCoordinate {
	return ParseGradleDependencyCoordinates(content, gradleLanguageForPath(path))
}

func ParseGradleDependencyCoordinates(content string, language *sitter.Language) []GradleDependencyCoordinate {
	if language == nil || strings.TrimSpace(content) == "" {
		return nil
	}
	parser := sitter.NewParser()
	parser.SetLanguage(language)
	tree, err := parser.ParseCtx(context.Background(), nil, []byte(content))
	if err != nil || tree == nil {
		return nil
	}
	source := []byte(content)
	coordinates := make([]GradleDependencyCoordinate, 0)
	walkGradleNode(tree.RootNode(), func(node *sitter.Node) {
		if !isGradleDependencyCall(node, source) {
			return
		}
		if coordinate, ok := gradleCoordinateFromCall(node, source); ok {
			coordinates = append(coordinates, coordinate)
		}
	})
	return coordinates
}

func parseGradleCatalogReferencesForFile(path, content string) []gradleCatalogReference {
	language := gradleLanguageForPath(path)
	if language == nil || strings.TrimSpace(content) == "" {
		return nil
	}
	parser := sitter.NewParser()
	parser.SetLanguage(language)
	tree, err := parser.ParseCtx(context.Background(), nil, []byte(content))
	if err != nil || tree == nil {
		return nil
	}
	source := []byte(content)
	references := make([]gradleCatalogReference, 0)
	walkGradleNode(tree.RootNode(), func(node *sitter.Node) {
		if !isGradleDependencyCall(node, source) {
			return
		}
		for _, arg := range gradleCallArguments(node) {
			for _, expression := range gradleDependencyArgumentExpressions(arg, source) {
				reference, ok := parseGradleCatalogReferenceExpression(expression)
				if ok {
					references = append(references, reference)
				}
			}
		}
	})
	return references
}

func gradleLanguageForPath(path string) *sitter.Language {
	if strings.EqualFold(filepath.Ext(path), ".kts") {
		return kotlin.GetLanguage()
	}
	return groovy.GetLanguage()
}

func walkGradleNode(node *sitter.Node, visit func(*sitter.Node)) {
	if node == nil {
		return
	}
	visit(node)
	for i := 0; i < int(node.NamedChildCount()); i++ {
		walkGradleNode(node.NamedChild(i), visit)
	}
}

func isGradleDependencyCall(node *sitter.Node, source []byte) bool {
	name, ok := gradleCallName(node, source)
	if !ok {
		return false
	}
	_, ok = gradleDependencyConfigurations[name]
	return ok
}

func gradleCallName(node *sitter.Node, source []byte) (string, bool) {
	switch node.Type() {
	case "call_expression":
		first := node.NamedChild(0)
		if first == nil || first.Type() != "simple_identifier" {
			return "", false
		}
		return gradleNodeText(first, source), true
	case "function_call", "juxt_function_call":
		first := node.NamedChild(0)
		if first == nil || first.Type() != "identifier" {
			return "", false
		}
		return gradleNodeText(first, source), true
	default:
		return "", false
	}
}

func gradleCoordinateFromCall(node *sitter.Node, source []byte) (GradleDependencyCoordinate, bool) {
	args := gradleCallArguments(node)
	fields := make(map[string]string)
	for _, arg := range args {
		if coordinate, ok := gradleCoordinateFromArgument(arg, source); ok {
			return coordinate, true
		}
		collectGradleNamedArgument(fields, arg, source)
	}
	return gradleCoordinateFromFields(fields)
}

func gradleCallArguments(node *sitter.Node) []*sitter.Node {
	if node == nil || node.NamedChildCount() < 2 {
		return nil
	}
	container := node.NamedChild(1)
	if container == nil {
		return nil
	}
	switch container.Type() {
	case "call_suffix":
		return gradleCallSuffixArguments(container)
	case "argument_list":
		return gradleNamedChildren(container)
	default:
		return gradleNamedChildren(container)
	}
}

func gradleCallSuffixArguments(node *sitter.Node) []*sitter.Node {
	args := make([]*sitter.Node, 0)
	for _, child := range gradleNamedChildren(node) {
		if child.Type() == "value_arguments" {
			args = append(args, gradleNamedChildren(child)...)
			continue
		}
		args = append(args, child)
	}
	return args
}

func gradleNamedChildren(node *sitter.Node) []*sitter.Node {
	if node == nil {
		return nil
	}
	children := make([]*sitter.Node, 0, int(node.NamedChildCount()))
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child != nil {
			children = append(children, child)
		}
	}
	return children
}

func gradleCoordinateFromArgument(node *sitter.Node, source []byte) (GradleDependencyCoordinate, bool) {
	if node == nil {
		return GradleDependencyCoordinate{}, false
	}
	if text, ok := gradleStringValue(node, source); ok {
		return parseGradleCoordinate(text)
	}
	if isGradlePlatformCall(node, source) {
		for _, arg := range gradleCallArguments(node) {
			if text, ok := gradleStringValue(arg, source); ok {
				return parseGradleCoordinate(text)
			}
		}
	}
	if node.Type() == "value_argument" && node.NamedChildCount() == 1 {
		return gradleCoordinateFromArgument(node.NamedChild(0), source)
	}
	return GradleDependencyCoordinate{}, false
}

func gradleDependencyArgumentExpressions(node *sitter.Node, source []byte) []string {
	if node == nil {
		return nil
	}
	if node.Type() == "value_argument" && node.NamedChildCount() == 1 {
		return gradleDependencyArgumentExpressions(node.NamedChild(0), source)
	}
	if isGradlePlatformCall(node, source) {
		expressions := make([]string, 0)
		for _, arg := range gradleCallArguments(node) {
			expressions = append(expressions, gradleDependencyArgumentExpressions(arg, source)...)
		}
		return expressions
	}
	if _, ok := gradleStringValue(node, source); ok {
		return nil
	}
	return []string{gradleNodeText(node, source)}
}

func isGradlePlatformCall(node *sitter.Node, source []byte) bool {
	name, ok := gradleCallName(node, source)
	if !ok {
		return false
	}
	return name == "platform" || name == "enforcedPlatform"
}

func gradleStringValue(node *sitter.Node, source []byte) (string, bool) {
	if node == nil {
		return "", false
	}
	switch node.Type() {
	case "string", "string_literal":
		return gradleStringLiteralText(node, source)
	case "value_argument":
		if node.NamedChildCount() == 1 {
			return gradleStringValue(node.NamedChild(0), source)
		}
	}
	return "", false
}

func gradleStringLiteralText(node *sitter.Node, source []byte) (string, bool) {
	parts := make([]string, 0)
	walkGradleNode(node, func(child *sitter.Node) {
		if child.Type() == "string_content" {
			parts = append(parts, gradleNodeText(child, source))
		}
	})
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, ""), true
}

func collectGradleNamedArgument(fields map[string]string, node *sitter.Node, source []byte) {
	if node == nil {
		return
	}
	switch node.Type() {
	case "map_item":
		if node.NamedChildCount() < 2 {
			return
		}
		key := gradleNodeText(node.NamedChild(0), source)
		value, ok := gradleStringValue(node.NamedChild(1), source)
		if ok {
			fields[strings.ToLower(strings.TrimSpace(key))] = value
		}
	case "value_argument":
		if node.NamedChildCount() < 2 {
			return
		}
		keyNode := node.NamedChild(0)
		if keyNode == nil || keyNode.Type() != "simple_identifier" {
			return
		}
		value, ok := gradleStringValue(node.NamedChild(1), source)
		if ok {
			fields[strings.ToLower(strings.TrimSpace(gradleNodeText(keyNode, source)))] = value
		}
	}
}

func gradleCoordinateFromFields(fields map[string]string) (GradleDependencyCoordinate, bool) {
	group := strings.TrimSpace(fields["group"])
	artifact := strings.TrimSpace(fields["name"])
	if group == "" || artifact == "" {
		return GradleDependencyCoordinate{}, false
	}
	return GradleDependencyCoordinate{
		Group:    group,
		Artifact: artifact,
		Version:  strings.TrimSpace(fields["version"]),
	}, true
}

func parseGradleCoordinate(value string) (GradleDependencyCoordinate, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 && len(parts) != 3 {
		return GradleDependencyCoordinate{}, false
	}
	group := strings.TrimSpace(parts[0])
	artifact := strings.TrimSpace(parts[1])
	if group == "" || artifact == "" {
		return GradleDependencyCoordinate{}, false
	}
	coordinate := GradleDependencyCoordinate{
		Group:    group,
		Artifact: artifact,
	}
	if len(parts) == 3 {
		coordinate.Version = strings.TrimSpace(parts[2])
	}
	return coordinate, true
}

func parseGradleCatalogReferenceExpression(expression string) (gradleCatalogReference, bool) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return gradleCatalogReference{}, false
	}
	if reference, ok := parseGradleCatalogFinderExpression(expression); ok {
		return reference, true
	}
	if reference, ok := parseGradleCatalogBracketExpression(expression); ok {
		return reference, true
	}
	return parseGradleCatalogPropertyExpression(expression)
}

func parseGradleCatalogFinderExpression(expression string) (gradleCatalogReference, bool) {
	clean := stripGradleExpressionSpaces(expression)
	for _, method := range []string{".findLibrary(", ".findBundle("} {
		index := strings.Index(clean, method)
		if index < 1 {
			continue
		}
		catalogName := clean[:index]
		alias, ok := firstGradleQuotedValue(clean[index+len(method):])
		if !ok {
			return gradleCatalogReference{}, false
		}
		return gradleCatalogReference{
			catalogName: normalizeGradleCatalogName(catalogName),
			alias:       normalizeGradleCatalogAccessor(alias),
			bundle:      method == ".findBundle(",
		}, true
	}
	return gradleCatalogReference{}, false
}

func parseGradleCatalogBracketExpression(expression string) (gradleCatalogReference, bool) {
	clean := stripGradleExpressionSpaces(expression)
	index := strings.Index(clean, "[")
	if index < 1 {
		return gradleCatalogReference{}, false
	}
	alias, ok := firstGradleQuotedValue(clean[index+1:])
	if !ok {
		return gradleCatalogReference{}, false
	}
	return gradleCatalogReference{
		catalogName: normalizeGradleCatalogName(clean[:index]),
		alias:       normalizeGradleCatalogAccessor(alias),
	}, true
}

func parseGradleCatalogPropertyExpression(expression string) (gradleCatalogReference, bool) {
	clean := normalizeGradleCatalogExpression(expression)
	if clean == "" || strings.Contains(clean, ".findlibrary") || strings.Contains(clean, ".findbundle") {
		return gradleCatalogReference{}, false
	}
	segments := strings.Split(clean, ".")
	if len(segments) < 2 {
		return gradleCatalogReference{}, false
	}
	reference := gradleCatalogReference{catalogName: normalizeGradleCatalogName(segments[0])}
	switch {
	case len(segments) >= 3 && segments[1] == "bundles":
		reference.alias = normalizeGradleCatalogAccessor(strings.Join(segments[2:], "."))
		reference.bundle = true
	case len(segments) >= 3 && (segments[1] == "versions" || segments[1] == "plugins"):
		reference.unsupportedExpression = clean
	default:
		reference.alias = normalizeGradleCatalogAccessor(strings.Join(segments[1:], "."))
	}
	return reference, true
}

func stripGradleExpressionSpaces(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, r := range value {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func firstGradleQuotedValue(value string) (string, bool) {
	quote := rune(0)
	var builder strings.Builder
	for _, r := range value {
		if quote == 0 {
			if r == '\'' || r == '"' {
				quote = r
			}
			continue
		}
		if r == quote {
			return builder.String(), true
		}
		builder.WriteRune(r)
	}
	return "", false
}

func gradleNodeText(node *sitter.Node, source []byte) string {
	if node == nil {
		return ""
	}
	return string(source[node.StartByte():node.EndByte()])
}
