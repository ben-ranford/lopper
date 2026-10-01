package reusecheck

import (
	"strings"
	"testing"
)

func TestMixedPointerReceiverIdentity(t *testing.T) {
	for _, receiver := range []struct{ parameter, implicit string }{
		{"raw *s.DependencyStats", "raw"},
		{"raw **s.DependencyStats", "(*raw)"},
		{"values []*s.DependencyStats", "values[0]"},
		{"holder struct { stats *s.DependencyStats }", "holder.stats"},
		{"raw any", "raw.(*s.DependencyStats)"},
		{"raw *s.DependencyStats", "(*s.DependencyStats)(raw)"},
	} {
		checkPointerReceiverIdentity(t, "", receiver.parameter, receiver.implicit, "(*"+receiver.implicit+")", "violation")
	}
	for _, read := range []string{"[0]", "[:][0]"} {
		checkPointerReceiverIdentity(t, "", "values *[2]*s.DependencyStats", "values"+read, "(*values)"+read, "violation")
	}
	for _, address := range []string{"(&raw)", "(*(&raw))"} {
		checkPointerReceiverIdentity(t, "", "raw s.DependencyStats", "raw", address, "violation")
	}
	checkPointerReceiverIdentity(t, "", "raw *s.DependencyStats", "raw", "(&(*raw))", "violation")
}

func TestMixedPointerSourceTypes(t *testing.T) {
	for _, declaration := range []string{
		"type StatsPtr = *s.DependencyStats", "type StatsPtr *s.DependencyStats",
		"type Base *s.DependencyStats; type StatsPtr Base",
	} {
		checkPointerReceiverIdentity(t, declaration, "raw StatsPtr", "raw", "(*raw)", "violation")
	}
	checkPointerReceiverIdentity(t, "type StatsPtr[T any] *s.DependencyStats", "raw StatsPtr[int]", "raw", "(*raw)", "violation")
	checkPointerReceiverIdentity(t, "type Holder struct { stats s.DependencyStats }", "h *Holder", "h.stats", "(*h).stats", "violation")
	checkPointerReceiverIdentity(t, "type Holder struct { *s.DependencyStats }", "h *Holder", "h", "(*h)", "violation")
	checkPointerReceiverIdentity(t, "type Inner struct { *s.DependencyStats }; type Holder struct { *Inner }", "h *Holder", "h", "(*(*h).Inner)", "violation")
	checkPointerReceiverIdentity(t, "type Inner struct { *s.DependencyStats }; type Holder struct { *Inner }", "h *Holder", "h", "h.Inner.DependencyStats", "violation")
}

func TestMixedPointerIdentityGuards(t *testing.T) {
	checkPointerReceiverIdentity(t, "", "raw, other *s.DependencyStats", "raw", "(*other)", "advisory")
	checkPointerReceiverIdentity(t, "", "raw **s.DependencyStats", "(*raw)", "raw", "advisory")
	checkPointerReceiverIdentity(t, "", "raw *s.DependencyStats", "raw", "(**raw)", "advisory")
	checkPointerReceiverIdentity(t, "type Other s.DependencyStats", "raw *s.DependencyStats, other *Other", "raw", "(*other)", "advisory")
}

func TestMixedPointerIdentityKeepsEffects(t *testing.T) {
	source := strings.NewReplacer("measured s.DependencyStats", "measured *s.DependencyStats", "measured.UsedCount", "(*measured).UsedCount").Replace(mappingFixture)
	for _, name := range []string{"mutate(measured)", "<-names"} {
		changed := strings.Replace(source, "Name:name", "Name:"+name, 1)
		changed += "\nvar names chan string; func mutate(*s.DependencyStats) string { return \"changed\" }"
		checkPromotedReportFindings(t, map[string][]byte{"fixture.go": []byte(changed)}, "advisory")
	}
}

func checkPointerReceiverIdentity(t *testing.T, declaration, parameter, first, other, want string) {
	t.Helper()
	provider := "package fixture; import s \"" + sharedPackage + "\"; " + declaration
	for _, field := range reportFields {
		for _, reverse := range []bool{false, true} {
			base, alternate := first, other
			if reverse {
				base, alternate = other, first
			}
			source := strings.NewReplacer("measured s.DependencyStats", parameter, "measured.", base+".").Replace(mappingFixture)
			source = strings.Replace(source, base+"."+field, alternate+"."+field, 1)
			if want == "advisory" && reverse {
				continue // An unproven majority need not reach the advisory threshold.
			}
			checkPromotedReportFindings(t, map[string][]byte{"fixture.go": []byte(source + "\n" + declaration)}, want)
			checkPromotedReportFindings(t, map[string][]byte{"types.go": []byte(provider), "reader.go": []byte(source)}, want)
		}
	}
}
