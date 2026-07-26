package gitexec

import (
	"fmt"
	"strings"
)

const (
	windowsGenericWrite        = uint32(0x40000000)
	windowsGenericAll          = uint32(0x10000000)
	windowsDelete              = uint32(0x00010000)
	windowsWriteDACL           = uint32(0x00040000)
	windowsWriteOwner          = uint32(0x00080000)
	windowsFileWriteData       = uint32(0x00000002)
	windowsFileAppendData      = uint32(0x00000004)
	windowsFileWriteEA         = uint32(0x00000010)
	windowsFileDeleteChild     = uint32(0x00000040)
	windowsFileWriteAttributes = uint32(0x00000100)
)

const windowsUntrustedWriteMask = windowsGenericWrite |
	windowsGenericAll |
	windowsDelete |
	windowsWriteDACL |
	windowsWriteOwner |
	windowsFileWriteData |
	windowsFileAppendData |
	windowsFileWriteEA |
	windowsFileDeleteChild |
	windowsFileWriteAttributes

const windowsUntrustedStructuralMask = windowsUntrustedWriteMask &
	^(windowsFileWriteData | windowsFileAppendData)

type windowsAccessEntry struct {
	principal   string
	mask        uint32
	inheritOnly bool
}

func validateWindowsAccessPolicy(owner string, entries []windowsAccessEntry) error {
	return validateWindowsAccessPolicyMask(owner, entries, windowsUntrustedWriteMask)
}

func validateWindowsStructuralPolicy(owner string, entries []windowsAccessEntry) error {
	return validateWindowsAccessPolicyMask(owner, entries, windowsUntrustedStructuralMask)
}

func validateWindowsAccessPolicyMask(owner string, entries []windowsAccessEntry, writeMask uint32) error {
	if !trustedWindowsPrincipal(owner) {
		return fmt.Errorf("untrusted Windows owner: %s", owner)
	}
	for _, entry := range entries {
		if entry.inheritOnly || entry.mask&writeMask == 0 {
			continue
		}
		if !trustedWindowsAccessPrincipal(entry.principal, owner) {
			return fmt.Errorf("untrusted Windows principal %s has write access", entry.principal)
		}
	}
	return nil
}

func trustedWindowsAccessPrincipal(principal, owner string) bool {
	return trustedWindowsPrincipal(principal) ||
		strings.EqualFold(principal, owner) ||
		principal == "S-1-3-0" ||
		principal == "S-1-3-4"
}

func trustedWindowsPrincipal(principal string) bool {
	switch strings.ToUpper(principal) {
	case "S-1-5-18",
		"S-1-5-32-544",
		"S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464":
		return true
	default:
		return false
	}
}
