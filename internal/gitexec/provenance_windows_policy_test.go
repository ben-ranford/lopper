package gitexec

import (
	"strings"
	"testing"
)

func TestValidateWindowsAccessPolicy(t *testing.T) {
	const (
		systemSID = "S-1-5-18"
		usersSID  = "S-1-5-32-545"
	)
	for _, tc := range []struct {
		name    string
		owner   string
		entries []windowsAccessEntry
		wantErr string
	}{
		{
			name:  "trusted owner with read-only users",
			owner: systemSID,
			entries: []windowsAccessEntry{
				{principal: usersSID, mask: 0x00120089},
			},
		},
		{
			name:  "trusted principal may write",
			owner: systemSID,
			entries: []windowsAccessEntry{
				{principal: "S-1-5-32-544", mask: windowsGenericAll},
			},
		},
		{
			name:  "inherit-only untrusted grant does not apply",
			owner: systemSID,
			entries: []windowsAccessEntry{
				{principal: usersSID, mask: windowsGenericWrite, inheritOnly: true},
			},
		},
		{
			name:    "untrusted owner",
			owner:   "S-1-5-21-1-2-3-1001",
			wantErr: "untrusted Windows owner",
		},
		{
			name:  "untrusted write grant",
			owner: systemSID,
			entries: []windowsAccessEntry{
				{principal: usersSID, mask: windowsFileWriteData},
			},
			wantErr: "has write access",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWindowsAccessPolicy(tc.owner, tc.entries)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("validate Windows access policy: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("expected %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidateWindowsStructuralPolicyAllowsSiblingCreationButRejectsReplacement(t *testing.T) {
	const (
		systemSID = "S-1-5-18"
		usersSID  = "S-1-5-32-545"
	)
	if err := validateWindowsStructuralPolicy(systemSID, []windowsAccessEntry{{
		principal: usersSID,
		mask:      windowsFileWriteData | windowsFileAppendData,
	}}); err != nil {
		t.Fatalf("volume sibling creation should not imply Program Files replacement: %v", err)
	}
	err := validateWindowsStructuralPolicy(systemSID, []windowsAccessEntry{{
		principal: usersSID,
		mask:      windowsFileDeleteChild,
	}})
	if err == nil {
		t.Fatal("expected volume child-replacement permission rejection")
	}
}
