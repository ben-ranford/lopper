package reusecheck

import "testing"

const definedStatsPointer = "type StatsPtr *s.DependencyStats"

func TestDefinedStatsPointerReceivers(t *testing.T) {
	for _, declaration := range []string{
		definedStatsPointer,
		"type Base *s.DependencyStats; type StatsPtr Base",
		"type Stats = s.DependencyStats; type StatsPtr *Stats",
		"type Base *s.DependencyStats; type StatsPtr = Base",
	} {
		checkCollectionProvenance(t, declaration, collectionProvenanceUse{"measured StatsPtr", "", "measured", false}, true)
	}
	checkCollectionProvenance(t, "type StatsPtr[T any] *s.DependencyStats", collectionProvenanceUse{"measured StatsPtr[int]", "", "measured", false}, true)
	checkCollectionProvenance(t, definedStatsPointer, collectionProvenanceUse{"raw StatsPtr", "measured := raw", "measured", false}, true)
}

func TestDefinedStatsPointerSources(t *testing.T) {
	for _, source := range []struct{ parameter, receiver string }{
		{"raw *s.DependencyStats", "StatsPtr(raw)"},
		{"raw any", "raw.(StatsPtr)"},
		{"raw StatsPtr", "(*raw)"},
		{"raw *StatsPtr", "(*raw)"},
		{"values []StatsPtr", "values[0]"},
		{"holder struct { stats StatsPtr }", "holder.stats"},
	} {
		checkCollectionProvenance(t, definedStatsPointer, collectionProvenanceUse{source.parameter, "", source.receiver, false}, true)
	}
	checkCollectionProvenance(t, definedStatsPointer, collectionProvenanceUse{"values []StatsPtr", "for _, measured := range values {", "measured", true}, true)
	checkCollectionProvenance(t, definedStatsPointer+"; func factory() StatsPtr { panic(0) }", collectionProvenanceUse{"unused string", "measured := factory()", "measured", false}, true)
	checkCollectionProvenance(t, definedStatsPointer+"; type Outer *StatsPtr", collectionProvenanceUse{"raw Outer", "", "(*raw)", false}, true)
}

func TestDefinedStatsPointerOwnershipGuards(t *testing.T) {
	for _, declaration := range []string{
		"type StatsPtr s.DependencyStats",
		"type Stats s.DependencyStats; type StatsPtr *Stats",
		"type Stats s.DependencyStats; type Base *Stats; type StatsPtr Base",
		"type StatsPtr **s.DependencyStats",
		"type Inner *s.DependencyStats; type StatsPtr *Inner",
		"type StatsPtr *missing.DependencyStats",
		"type StatsPtr *StatsPtr",
		"type StatsPtr Other; type Other StatsPtr",
		"type StatsPtr = *StatsPtr",
	} {
		checkCollectionProvenance(t, declaration, collectionProvenanceUse{"measured StatsPtr", "", "measured", false}, false)
	}
	checkCollectionProvenance(t, definedStatsPointer, collectionProvenanceUse{"measured *StatsPtr", "", "measured", false}, false)
	checkCollectionProvenance(t, "type StatsPtr[T any] *T", collectionProvenanceUse{"measured StatsPtr[s.DependencyStats]", "", "measured", false}, false)
	checkCollectionProvenance(t, "type StatsPtr[T any] *s.DependencyStats", collectionProvenanceUse{"measured StatsPtr", "", "measured", false}, false)
}
