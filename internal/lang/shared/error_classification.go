package shared

import "github.com/ben-ranford/lopper/internal/errutil"

// IsPureSentinelError reports whether every terminal error in err's unwrap
// tree matches at least one sentinel.
func IsPureSentinelError(err error, sentinels ...error) bool {
	return errutil.IsPureSentinelError(err, sentinels...)
}
