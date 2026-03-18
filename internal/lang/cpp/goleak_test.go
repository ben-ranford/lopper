package cpp

import (
	"testing"

	"github.com/ben-ranford/lopper/internal/testsupport"
)

func TestMain(m *testing.M) {
	testsupport.RunOptionalLeakMain(m)
}
