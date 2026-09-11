//go:build regressionproof

package dotnet

import "testing"

func TestDotNetDiscoverScanInputsDoesNotRetainSourceDocuments(t *testing.T) {
	assertDotNetScanDoesNotRetainSourceDocuments(t)
}
