package js

import (
	"errors"
	"fmt"
	"io"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func closeRootAppendWarning(root safeio.Root, warnings *[]string, warning string) bool {
	if closeErr := root.Close(); closeErr != nil {
		*warnings = append(*warnings, fmt.Sprintf("%s: %v", warning, closeErr))
		return true
	}
	return false
}

func closeRootResetResolution(root safeio.Root, resolved *string, ok *bool, warning string) error {
	if closeErr := root.Close(); closeErr != nil {
		*resolved = ""
		*ok = false
		return fmt.Errorf("%s: %w", warning, closeErr)
	}
	return nil
}

func closeRootResetSlice(root safeio.Root, values *[]string, warning string) error {
	if closeErr := root.Close(); closeErr != nil {
		*values = nil
		return fmt.Errorf("%s: %w", warning, closeErr)
	}
	return nil
}

func closeRootResetLicense(root safeio.Root, license **report.DependencyLicense, warning string) error {
	if closeErr := root.Close(); closeErr != nil {
		*license = nil
		return fmt.Errorf("%s: %w", warning, closeErr)
	}
	return nil
}

func closeRootResetProbe(root safeio.Root, probe **licenseFileProbe, warning string) error {
	if closeErr := root.Close(); closeErr != nil {
		*probe = nil
		return fmt.Errorf("%s: %w", warning, closeErr)
	}
	return nil
}

func closeReadCloserPreserveErr(closer io.Closer, err *error) {
	if closeErr := closer.Close(); closeErr != nil {
		*err = errors.Join(*err, closeErr)
	}
}
