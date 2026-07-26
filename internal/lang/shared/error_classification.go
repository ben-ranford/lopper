package shared

import "errors"

type multiError interface {
	error
	Unwrap() []error
}

// IsPureSentinelError reports whether every terminal error in err's unwrap
// tree matches at least one sentinel.
func IsPureSentinelError(err error, sentinels ...error) bool {
	if err == nil || len(sentinels) == 0 {
		return false
	}
	var wrapped multiError
	if errors.As(err, &wrapped) {
		return arePureSentinelCauses(wrapped.Unwrap(), sentinels)
	}
	if cause := errors.Unwrap(err); cause != nil {
		return IsPureSentinelError(cause, sentinels...)
	}
	return matchesSentinel(err, sentinels)
}

func arePureSentinelCauses(causes []error, sentinels []error) bool {
	found := false
	for _, cause := range causes {
		if cause == nil {
			continue
		}
		found = true
		if !IsPureSentinelError(cause, sentinels...) {
			return false
		}
	}
	return found
}

func matchesSentinel(err error, sentinels []error) bool {
	for _, sentinel := range sentinels {
		if sentinel != nil && errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}
