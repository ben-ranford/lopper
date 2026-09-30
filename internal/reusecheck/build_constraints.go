package reusecheck

import (
	"bytes"
	"go/build/constraint"
	"go/parser"
	"go/scanner"
	"go/token"
	"maps"
	"path/filepath"
	"strconv"
	"strings"
)

type sourceBuildPredicate struct {
	term, inverse *buildTerm
	valid, ready  bool
	witness       bool
	states        []buildWorldFacts
}

type buildOperation uint8

const (
	buildTag buildOperation = iota
	buildAnd
	buildOr
	buildTrue
	buildFalse
)

type buildTerm struct {
	op          buildOperation
	tag         string
	negative    bool
	left, right *buildTerm
}

type buildWorld struct{ os, arch string }

type buildWorldFacts struct {
	world buildWorld
	facts map[string]bool
}

type buildFacts struct {
	possible bool
	values   map[string]bool
}

// Filename tables follow Go 1.27's src/internal/syslist/syslist.go. Historical
// targets remain meaningful filename suffixes even after their ports disappear.
var buildKnownOS = strings.Fields("aix android darwin dragonfly freebsd hurd illumos ios js linux nacl netbsd openbsd plan9 solaris wasip1 windows zos")
var buildKnownArch = strings.Fields("386 amd64 amd64p32 arm armbe arm64 arm64be loong64 mips mipsle mips64 mips64le mips64p32 mips64p32le ppc ppc64 ppc64le riscv riscv64 s390 s390x sparc sparc64 wasm")
var buildUnixOS = " aix android darwin dragonfly freebsd hurd illumos ios linux netbsd openbsd solaris "

func sourceBuildConstraint(path string, source []byte) *sourceBuildPredicate {
	expression, valid := sourceBuildHeader(source)
	name := filepath.Base(path)
	valid = valid && !strings.HasPrefix(name, ".") && !strings.HasPrefix(name, "_")
	if suffix := sourceBuildFilename(path); suffix != nil {
		expression = buildConstraintAnd(expression, suffix)
	}
	if sourceImportsC(source) {
		expression = buildConstraintAnd(expression, &constraint.TagExpr{Tag: "cgo"})
	}
	return &sourceBuildPredicate{
		term: buildConstraintTerm(expression, false), inverse: buildConstraintTerm(expression, true), valid: valid,
	}
}

func (p *sourceBuildPredicate) unconditional() bool {
	return p.valid && p.term.op == buildTrue
}

func (p *sourceBuildPredicate) implies(other *sourceBuildPredicate) bool {
	return p.proves(other, true)
}

func (p *sourceBuildPredicate) excludes(other *sourceBuildPredicate) bool {
	return p.proves(other, false)
}

func (p *sourceBuildPredicate) proves(other *sourceBuildPredicate, implication bool) bool {
	if !p.valid || !other.valid {
		return false
	}
	p.prepare()
	other.prepare()
	if !p.witness || !other.witness {
		return false
	}
	if implication && equalBuildTerms(p.term, other.term) {
		return true
	}
	right := other.term
	if implication {
		right = other.inverse
	}
	combination := &buildTerm{op: buildAnd, left: p.term, right: right}
	for _, state := range p.states {
		if refineBuildFacts(combination, state.world, state.facts).possible {
			return false
		}
	}
	return true
}

// Enumerating the fixed platform domain does not enumerate custom tag choices.
// The extra worlds cover future platforms, including either Unix possibility.
func (p *sourceBuildPredicate) prepare() {
	if p.ready {
		return
	}
	p.ready = true
	systems := append(append([]string(nil), buildKnownOS...), "other", "other-unix")
	architectures := append(append([]string(nil), buildKnownArch...), "other")
	for _, system := range systems {
		for _, architecture := range architectures {
			p.addWorld(buildWorld{os: system, arch: architecture})
		}
	}
}

func (p *sourceBuildPredicate) addWorld(world buildWorld) {
	facts := refineBuildFacts(p.term, world, nil)
	if !facts.possible {
		return
	}
	p.states = append(p.states, buildWorldFacts{world: world, facts: facts.values})
	if !p.witness {
		p.witness = buildWitness(p.term, world, maps.Clone(facts.values))
	}
}

// Each pass adds a necessary custom literal or stops. This is polynomial in
// source size, and an unresolved disjunction never becomes an invented fact.
func refineBuildFacts(term *buildTerm, world buildWorld, initial map[string]bool) buildFacts {
	known := make(map[string]bool)
	maps.Copy(known, initial)
	for {
		facts := collectBuildFacts(term, world, known)
		if !facts.possible {
			return facts
		}
		before := len(known)
		maps.Copy(known, facts.values)
		if len(known) == before {
			return buildFacts{possible: true, values: known}
		}
	}
}

func collectBuildFacts(term *buildTerm, world buildWorld, known map[string]bool) buildFacts {
	switch term.op {
	case buildTrue:
		return buildFacts{possible: true}
	case buildFalse:
		return buildFacts{}
	case buildTag:
		if value, exists := buildTagValue(term.tag, world, known); exists {
			return buildFacts{possible: value != term.negative}
		}
		return buildFacts{possible: true, values: map[string]bool{term.tag: !term.negative}}
	default:
		left := collectBuildFacts(term.left, world, known)
		right := collectBuildFacts(term.right, world, known)
		if term.op == buildAnd {
			return intersectBuildFacts(left, right)
		}
		return unionBuildFacts(left, right)
	}
}

func intersectBuildFacts(left, right buildFacts) buildFacts {
	if !left.possible || !right.possible {
		return buildFacts{}
	}
	values := make(map[string]bool)
	maps.Copy(values, left.values)
	for name, value := range right.values {
		if previous, exists := values[name]; exists && previous != value {
			return buildFacts{}
		}
		values[name] = value
	}
	return buildFacts{possible: true, values: values}
}

func unionBuildFacts(left, right buildFacts) buildFacts {
	if !left.possible {
		return right
	}
	if !right.possible {
		return left
	}
	values := make(map[string]bool)
	for name, value := range left.values {
		if other, exists := right.values[name]; exists && other == value {
			values[name] = value
		}
	}
	return buildFacts{possible: true, values: values}
}

// Greedy branch choices visit each expression node at most once. Failure is
// unknown, not unsatisfiability; success supplies an actual Boolean witness.
func buildWitness(term *buildTerm, world buildWorld, known map[string]bool) bool {
	switch term.op {
	case buildTrue:
		return true
	case buildFalse:
		return false
	case buildTag:
		if value, exists := buildTagValue(term.tag, world, known); exists {
			return value != term.negative
		}
		known[term.tag] = !term.negative
		return true
	case buildAnd:
		return buildWitness(term.left, world, known) && buildWitness(term.right, world, known)
	default:
		choice := maps.Clone(known)
		if buildWitness(term.left, world, choice) {
			maps.Copy(known, choice)
			return true
		}
		return buildWitness(term.right, world, known)
	}
}

func buildTagValue(tag string, world buildWorld, known map[string]bool) (bool, bool) {
	if containsBuildName(buildKnownOS, tag) {
		return world.os == tag || buildOSAlias(world.os) == tag, true
	}
	if containsBuildName(buildKnownArch, tag) {
		return world.arch == tag, true
	}
	if tag == "unix" {
		return world.os == "other-unix" || strings.Contains(buildUnixOS, " "+world.os+" "), true
	}
	value, exists := known[tag]
	return value, exists
}

func buildOSAlias(system string) string {
	switch system {
	case "android":
		return "linux"
	case "ios":
		return "darwin"
	case "illumos":
		return "solaris"
	default:
		return ""
	}
}

func containsBuildName(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

func equalBuildTerms(left, right *buildTerm) bool {
	if left.op != right.op || left.tag != right.tag || left.negative != right.negative {
		return false
	}
	if left.op != buildAnd && left.op != buildOr {
		return true
	}
	return equalBuildTerms(left.left, right.left) && equalBuildTerms(left.right, right.right)
}

func buildConstraintTerm(expression constraint.Expr, negative bool) *buildTerm {
	switch value := expression.(type) {
	case nil:
		if negative {
			return &buildTerm{op: buildFalse}
		}
		return &buildTerm{op: buildTrue}
	case *constraint.TagExpr:
		return &buildTerm{op: buildTag, tag: value.Tag, negative: negative}
	case *constraint.NotExpr:
		return buildConstraintTerm(value.X, !negative)
	case *constraint.AndExpr:
		return buildConstraintPair(value.X, value.Y, negative, !negative)
	case *constraint.OrExpr:
		return buildConstraintPair(value.X, value.Y, negative, negative)
	default:
		return &buildTerm{op: buildFalse}
	}
}

func buildConstraintPair(left, right constraint.Expr, negative, conjunction bool) *buildTerm {
	op := buildOr
	if conjunction {
		op = buildAnd
	}
	return &buildTerm{op: op, left: buildConstraintTerm(left, negative), right: buildConstraintTerm(right, negative)}
}

func buildConstraintAnd(left, right constraint.Expr) constraint.Expr {
	if left == nil {
		return right
	}
	return &constraint.AndExpr{X: left, Y: right}
}

// Match go/build.goodOSArchFile, including its first-dot and _test handling.
func sourceBuildFilename(path string) constraint.Expr {
	name, _, _ := strings.Cut(filepath.Base(path), ".")
	if index := strings.IndexByte(name, '_'); index >= 0 {
		name = name[index:]
	} else {
		return nil
	}
	parts := strings.Split(strings.TrimSuffix(name, "_test"), "_")
	last := parts[len(parts)-1]
	if len(parts) >= 2 && containsBuildName(buildKnownOS, parts[len(parts)-2]) && containsBuildName(buildKnownArch, last) {
		return &constraint.AndExpr{X: &constraint.TagExpr{Tag: parts[len(parts)-2]}, Y: &constraint.TagExpr{Tag: last}}
	}
	if containsBuildName(buildKnownOS, last) || containsBuildName(buildKnownArch, last) {
		return &constraint.TagExpr{Tag: last}
	}
	return nil
}

func sourceBuildHeader(source []byte) (constraint.Expr, bool) {
	source = bytes.TrimPrefix(source, []byte{0xef, 0xbb, 0xbf})
	file := token.NewFileSet().AddFile("build-header.go", -1, len(source))
	var scan scanner.Scanner
	scan.Init(file, source, nil, scanner.ScanComments)
	legacyEnd := legacyBuildHeaderEnd(source)
	var explicit, legacy []string
	for {
		position, kind, text := scan.Scan()
		if kind != token.COMMENT {
			break
		}
		if !standaloneBuildComment(source, file.Offset(position)) {
			continue
		}
		if constraint.IsGoBuild(text) {
			explicit = append(explicit, text)
		} else if file.Offset(position) < legacyEnd && constraint.IsPlusBuild(text) {
			legacy = append(legacy, text)
		}
	}
	return parseBuildHeader(explicit, legacy)
}

func standaloneBuildComment(source []byte, offset int) bool {
	start := bytes.LastIndexByte(source[:offset], '\n') + 1
	return len(bytes.TrimSpace(source[start:offset])) == 0
}

func legacyBuildHeaderEnd(source []byte) int {
	offset, end := 0, 0
	for _, line := range bytes.SplitAfter(source, []byte{'\n'}) {
		offset += len(line)
		text := bytes.TrimSpace(line)
		if len(text) == 0 {
			end = offset
		} else if !bytes.HasPrefix(text, []byte("//")) {
			break
		}
	}
	return end
}

func parseBuildHeader(explicit, legacy []string) (constraint.Expr, bool) {
	if len(explicit) > 1 {
		return nil, false
	}
	if len(explicit) == 1 {
		expression, err := constraint.Parse(explicit[0])
		return expression, err == nil
	}
	var expression constraint.Expr
	for _, line := range legacy {
		parsed, err := constraint.Parse(line)
		if err != nil {
			return nil, false
		}
		expression = buildConstraintAnd(expression, parsed)
	}
	return expression, true
}

func sourceImportsC(source []byte) bool {
	file, err := parser.ParseFile(token.NewFileSet(), "build-header.go", source, parser.ImportsOnly)
	if err != nil {
		return false
	}
	for _, imported := range file.Imports {
		if path, err := strconv.Unquote(imported.Path.Value); err == nil && path == "C" {
			return true
		}
	}
	return false
}
