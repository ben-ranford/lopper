package fixture

import "context"

func validContext(ctx context.Context, value int) {
	_, _ = ctx, value
}
