package fixture

import "context"

var invalidLiteral = func(value int, ctx context.Context) {
	_, _ = value, ctx
}
