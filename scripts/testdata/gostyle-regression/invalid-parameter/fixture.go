package fixture

import "context"

func invalidContext(value int, ctx context.Context) {
	_, _ = value, ctx
}
