//go:build !windows

package runtime

import (
	"os"
	"syscall"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestIsTrustedRuntimeSearchDirInfoAllowsUserOwnedGroupWritableDirectory(t *testing.T) {
	info, err := os.Stat(testutil.SecureHomeTempDir(t, "runtime-owned-group-write-"))
	if err != nil {
		t.Fatalf("stat trusted dir: %v", err)
	}
	groupWritable := fileInfoWithMode(info, os.ModeDir|0o775)
	if !isTrustedRuntimeSearchDirInfo(groupWritable) {
		t.Fatal("expected user-owned group-writable directory to remain trusted")
	}
}

func TestIsTrustedRuntimeSearchDirInfoRejectsForeignOwnedGroupWritableDirectory(t *testing.T) {
	info, err := os.Stat(testutil.SecureHomeTempDir(t, "runtime-foreign-group-write-"))
	if err != nil {
		t.Fatalf("stat trusted dir: %v", err)
	}
	groupWritable := fileInfoWithMode(info, os.ModeDir|0o775)
	foreignOwned := fileInfoWithStat(groupWritable, &syscall.Stat_t{Uid: uint32(os.Geteuid() + 1)})
	if isTrustedRuntimeSearchDirInfo(foreignOwned) {
		t.Fatal("expected foreign-owned group-writable directory to be rejected")
	}
}

func TestIsTrustedRuntimeSearchDirInfoRejectsGroupWritableDirectoryWithoutOwnership(t *testing.T) {
	info, err := os.Stat(testutil.SecureHomeTempDir(t, "runtime-missing-owner-"))
	if err != nil {
		t.Fatalf("stat trusted dir: %v", err)
	}
	groupWritable := fileInfoWithMode(info, os.ModeDir|0o775)
	withoutOwnership := fileInfoWithStat(groupWritable, nil)
	if isTrustedRuntimeSearchDirInfo(withoutOwnership) {
		t.Fatal("expected group-writable directory without ownership metadata to be rejected")
	}
}

func TestPlatformRuntimeWindowsExecutableRootsIsEmptyOnUnix(t *testing.T) {
	if roots := platformRuntimeWindowsExecutableRoots(); len(roots) != 0 {
		t.Fatalf("expected no Windows executable roots on Unix, got %v", roots)
	}
}

func TestTrustedRuntimeOwnerUIDRejectsOutOfRangeEffectiveUID(t *testing.T) {
	maxUID := ^uint32(0)
	if !trustedRuntimeOwnerUID(maxUID, int64(maxUID)) {
		t.Fatal("expected matching maximum uint32 UID to remain trusted")
	}
	if trustedRuntimeOwnerUID(maxUID, -1) {
		t.Fatal("expected negative effective UID to be rejected")
	}
	if trustedRuntimeOwnerUID(maxUID, int64(maxUID)+1) {
		t.Fatal("expected effective UID above uint32 range to be rejected")
	}
}

func TestTrustedRuntimePathEntryImmutableAccountsForRelevantGroups(t *testing.T) {
	info, err := os.Stat(testutil.SecureHomeTempDir(t, "runtime-immutable-group-"))
	if err != nil {
		t.Fatalf("stat immutable path fixture: %v", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("expected Unix stat metadata")
	}
	effectiveUID := int64(stat.Uid) + 1
	groupWritable := fileInfoWithStat(fileInfoWithMode(info, os.ModeDir|0o775), &syscall.Stat_t{Uid: stat.Uid, Gid: 42})

	if trustedRuntimePathEntryImmutable(groupWritable, effectiveUID, []int{42}) {
		t.Fatal("expected relevant group-writable path entry to remain replaceable")
	}
	if !trustedRuntimePathEntryImmutable(groupWritable, effectiveUID, []int{7}) {
		t.Fatal("expected unrelated group-writable path entry to be immutable")
	}
	worldWritable := fileInfoWithMode(groupWritable, os.ModeDir|0o777)
	if trustedRuntimePathEntryImmutable(worldWritable, effectiveUID, []int{7}) {
		t.Fatal("expected world-writable path entry to remain replaceable")
	}
}

func TestRuntimeGroupContains(t *testing.T) {
	if !runtimeGroupContains([]int{7, 42}, 42) {
		t.Fatal("expected matching runtime group")
	}
	if runtimeGroupContains([]int{7, 42}, 99) {
		t.Fatal("expected absent runtime group")
	}
}
