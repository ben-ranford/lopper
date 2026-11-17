package main

import (
	"strings"
	"testing"
)

func TestAliasDiscoveryRejectsShadowExtraMissingAndOverflow(t *testing.T) {
	bin := `D:\hostedtoolcache\windows\go\1.27.2\x64\bin`
	expected := []string{bin + `\python.exe`, bin + `\python3.exe`}
	good := strings.Join(expected, "\r\n") + "\r\n"
	if err := validateDiscovery([]byte(good), expected); err != nil {
		t.Fatal(err)
	}
	if err := validateDiscovery([]byte(strings.ToLower(good)), expected); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(good, bin, `C:\workspace`, 1), good + "extra\n", strings.TrimSpace(good), "", strings.Repeat("x", 2*maxStringBytes+5)} {
		if err := validateDiscovery([]byte(bad), expected); err == nil {
			t.Fatal("untrusted or incomplete native alias discovery accepted")
		}
	}
	if err := validateDiscovery([]byte(good), expected[:1]); err == nil {
		t.Fatal("single-alias admission accepted")
	}
}
