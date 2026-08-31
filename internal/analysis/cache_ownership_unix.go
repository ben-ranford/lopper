//go:build unix

package analysis

import (
	"io/fs"
	"syscall"
)

func sameAnalysisCacheRollbackOwner(currentInfo, childInfo fs.FileInfo) bool {
	currentStat, currentOK := currentInfo.Sys().(*syscall.Stat_t)
	childStat, childOK := childInfo.Sys().(*syscall.Stat_t)
	if !currentOK || !childOK {
		return true
	}
	return currentStat.Uid == childStat.Uid && currentStat.Gid == childStat.Gid
}
