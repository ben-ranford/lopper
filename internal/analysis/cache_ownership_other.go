//go:build !unix

package analysis

import "io/fs"

func sameAnalysisCacheRollbackOwner(currentInfo, childInfo fs.FileInfo) bool {
	return true
}
