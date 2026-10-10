//go:build windows

package model

import "testing"

func TestMavenWindowsHostPathRejection(t *testing.T) {
	for _, path := range []string{`C:/pom.xml`, `C:pom.xml`, `//server/share/pom.xml`, `a\pom.xml`, `a:b/pom.xml`, `what?/pom.xml`, `nul/pom.xml`, `line` + "\n" + `break/pom.xml`} {
		if err := ValidateMavenPath(path); err == nil {
			t.Fatalf("invalid host path accepted %q", path)
		}
	}
	if err := ValidateMavenPath("valid space/pom.xml"); err != nil {
		t.Fatal(err)
	}
}
