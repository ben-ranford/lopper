package reusecheck

import (
	"fmt"
	"testing"
)

func TestBuildPredicateCompositionIdentities(t *testing.T) {
	for _, tc := range []struct{ left, right, conjunction, disjunction string }{
		{"", "feature", "feature", ""},
		{"feature", "", "feature", ""},
		{"feature", "feature", "feature", "feature"},
		{"linux", "amd64", "linux && amd64", "linux || amd64"},
		{"a || b", "!a || c", "(a || b) && (!a || c)", "a || b || !a || c"},
	} {
		t.Run(tc.left+" with "+tc.right, func(t *testing.T) {
			left, right := taggedBuildPredicate(t, tc.left), taggedBuildPredicate(t, tc.right)
			assertBuildCompositionEquivalent(t, left.and(right), taggedBuildPredicate(t, tc.conjunction))
			assertBuildCompositionEquivalent(t, left.or(right), taggedBuildPredicate(t, tc.disjunction))
		})
	}
}

func TestBuildPredicateCompositionInvalid(t *testing.T) {
	invalid := taggedBuildPredicate(t, "linux &&")
	invalid.prepare()
	live := taggedBuildPredicate(t, "feature")
	for name, predicate := range map[string]*sourceBuildPredicate{
		"invalid left and":  invalid.and(live),
		"invalid right and": live.and(invalid),
		"invalid left or":   invalid.or(live),
		"invalid right or":  live.or(invalid),
		"invalid union":     unionSourceBuildPredicates([]*sourceBuildPredicate{live, invalid, live}),
	} {
		t.Run(name, func(t *testing.T) {
			predicate.prepare()
			if predicate.valid || predicate.unconditional() {
				t.Fatal("invalid operand produced an applicable predicate")
			}
			assertBuildCompositionNoProof(t, predicate, live)
		})
	}
}

func TestBuildPredicateCompositionImpossible(t *testing.T) {
	live := taggedBuildPredicate(t, "feature")
	empty := unionSourceBuildPredicates(nil)
	for name, predicate := range map[string]*sourceBuildPredicate{
		"empty union":            empty,
		"empty left and":         empty.and(live),
		"empty right and":        live.and(empty),
		"platform contradiction": taggedBuildPredicate(t, "linux").and(taggedBuildPredicate(t, "windows")),
		"custom contradiction":   live.and(taggedBuildPredicate(t, "!feature")),
	} {
		t.Run(name, func(t *testing.T) {
			assertBuildCompositionNoProof(t, predicate, live)
			assertBuildCompositionEquivalent(t, predicate.or(live), live)
			assertBuildCompositionEquivalent(t, live.or(predicate), live)
		})
	}
}

func TestBuildPredicateUnionDeduplicatesAndBalances(t *testing.T) {
	const unique = 64
	var predicates []*sourceBuildPredicate
	for index := range unique {
		for range 3 {
			predicates = append(predicates, taggedBuildPredicate(t, fmt.Sprintf("feature%d", index)))
		}
	}
	union := unionSourceBuildPredicates(predicates)
	for name, term := range map[string]*buildTerm{"expression": union.term, "inverse": union.inverse} {
		depth, leaves := buildCompositionTreeSize(term)
		if leaves != unique || depth > 7 {
			t.Errorf("%s has depth %d and %d leaves; want depth <= 7 and %d unique leaves", name, depth, leaves, unique)
		}
	}
	assertBuildCompositionEquivalent(t, unionSourceBuildPredicates([]*sourceBuildPredicate{predicates[0]}), predicates[0])
}

func TestBuildPlatformRegionsCacheExactWorlds(t *testing.T) {
	regions := buildCompositionRegions(t)
	for world, region := range regions {
		if !region.ready || !region.witness || !region.domain.restricted {
			t.Fatalf("region %v has no cached restricted witness", world)
		}
		if len(region.domain.worlds) != 1 || region.domain.worlds[0] != world {
			t.Fatalf("region %v has incorrect domain %v", world, region.domain.worlds)
		}
		region.prepare()
		if len(region.states) != 1 || region.states[0].world != world {
			t.Fatalf("preparing region %v changed its cached state", world)
		}
	}
}

func TestBuildPlatformRegionsKeepAliasesAndFutureWorlds(t *testing.T) {
	regions := buildCompositionRegions(t)
	for _, tc := range []struct{ system, implied, excluded string }{
		{"android", "android && linux && unix", "windows"},
		{"linux", "linux && unix", "android"},
		{"ios", "ios && darwin && unix", "linux"},
		{"darwin", "darwin && unix", "ios"},
		{"illumos", "illumos && solaris && unix", "linux"},
		{"solaris", "solaris && unix", "illumos"},
		{"other", "!unix && !linux && !windows", "unix"},
		{"other-unix", "unix && !linux && !windows", "windows"},
	} {
		t.Run(tc.system, func(t *testing.T) {
			region := regions[buildWorld{os: tc.system, arch: "other"}]
			if !region.implies(taggedBuildPredicate(t, tc.implied)) || !region.excludes(taggedBuildPredicate(t, tc.excluded)) {
				t.Fatal("exact region lost an alias or future platform distinction")
			}
			if !region.excludes(taggedBuildPredicate(t, "amd64 || arm64")) {
				t.Fatal("future architecture acquired a known architecture")
			}
		})
	}
}

func TestBuildPlatformRegionCompositionDomains(t *testing.T) {
	regions := buildCompositionRegions(t)
	linux := regions[buildWorld{os: "linux", arch: "amd64"}]
	windows := regions[buildWorld{os: "windows", arch: "arm64"}]
	union := linux.or(windows)
	assertBuildCompositionDomain(t, union, 2)
	assertBuildCompositionDomain(t, union.or(linux), 2)
	assertBuildCompositionDomain(t, union.and(linux), 1)
	assertBuildCompositionDomain(t, linux.and(windows), 0)
	assertBuildCompositionNoProof(t, linux.and(windows), linux)
	assertBuildCompositionEquivalent(t, union.and(taggedBuildPredicate(t, "amd64")), linux)
	assertBuildCompositionEquivalent(t, union.and(taggedBuildPredicate(t, "arm64")), windows)
	if !union.excludes(taggedBuildPredicate(t, "darwin")) {
		t.Fatal("union included an unrelated platform")
	}
	if union.or(taggedBuildPredicate(t, "feature")).domain.restricted {
		t.Fatal("union with an unrestricted custom tag retained a platform restriction")
	}
}

func TestBuildPlatformRegionCompositionPreservesFacts(t *testing.T) {
	region := buildCompositionRegions(t)[buildWorld{os: "linux", arch: "amd64"}]
	feature := taggedBuildPredicate(t, "feature")
	combined := region.and(feature)
	assertBuildCompositionDomain(t, combined, 1)
	if !combined.implies(feature) || !combined.implies(region) {
		t.Fatal("restricted conjunction lost an operand")
	}
	if region.implies(combined) || region.excludes(combined) {
		t.Fatal("conjunction changed the original region's custom tag facts")
	}
	equal := taggedBuildPredicate(t, "linux && !android && amd64")
	for _, predicate := range []*sourceBuildPredicate{equal.and(region), region.and(equal), equal.or(region), region.or(equal)} {
		assertBuildCompositionDomain(t, predicate, 1)
		if !predicate.ready || len(predicate.states) != 1 {
			t.Fatal("equivalent region composition discarded its cached state")
		}
	}
}

func TestBuildPlatformUnionCoversEveryTarget(t *testing.T) {
	regions := sourceBuildPlatformRegions()
	union := unionSourceBuildPredicates(regions)
	for _, expression := range []string{"", "android", "!linux", "unix", "!unix", "feature && cgo", "go1.99 || !go1.99", "!linux && amd64 && feature"} {
		t.Run(expression, func(t *testing.T) {
			if !taggedBuildPredicate(t, expression).implies(union) {
				t.Fatal("platform union failed to cover target")
			}
		})
	}
	var known []*sourceBuildPredicate
	for _, region := range regions {
		world := region.states[0].world
		if world.os != "other" && world.os != "other-unix" && world.arch != "other" {
			known = append(known, region)
		}
	}
	if taggedBuildPredicate(t, "").implies(unionSourceBuildPredicates(known)) {
		t.Fatal("known platforms incorrectly cover future platforms")
	}
}

func assertBuildCompositionEquivalent(t *testing.T, left, right *sourceBuildPredicate) {
	t.Helper()
	if !left.implies(right) || !right.implies(left) {
		t.Fatal("composition changed its expected truth conditions")
	}
}

func assertBuildCompositionNoProof(t *testing.T, predicate, live *sourceBuildPredicate) {
	t.Helper()
	if predicate.implies(live) || predicate.excludes(live) || live.implies(predicate) || live.excludes(predicate) {
		t.Fatal("invalid or impossible predicate established a proof")
	}
}

func assertBuildCompositionDomain(t *testing.T, predicate *sourceBuildPredicate, size int) {
	t.Helper()
	if !predicate.domain.restricted || len(predicate.domain.worlds) != size {
		t.Fatalf("domain = %v; want a restricted domain of size %d", predicate.domain, size)
	}
	predicate.prepare()
	if len(predicate.states) != size {
		t.Fatalf("cached states = %d; want %d", len(predicate.states), size)
	}
}

func buildCompositionTreeSize(term *buildTerm) (int, int) {
	if term.op != buildAnd && term.op != buildOr {
		return 1, 1
	}
	leftDepth, leftLeaves := buildCompositionTreeSize(term.left)
	rightDepth, rightLeaves := buildCompositionTreeSize(term.right)
	return 1 + max(leftDepth, rightDepth), leftLeaves + rightLeaves
}

func buildCompositionRegions(t *testing.T) map[buildWorld]*sourceBuildPredicate {
	t.Helper()
	regions := make(map[buildWorld]*sourceBuildPredicate)
	for _, region := range sourceBuildPlatformRegions() {
		if len(region.states) != 1 {
			t.Fatal("platform region does not have exactly one cached world")
		}
		world := region.states[0].world
		if regions[world] != nil {
			t.Fatalf("duplicate platform region %v", world)
		}
		regions[world] = region
	}
	if len(regions) != (len(buildKnownOS)+2)*(len(buildKnownArch)+1) {
		t.Fatal("platform regions omit known or future worlds")
	}
	return regions
}
