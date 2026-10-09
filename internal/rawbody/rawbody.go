// Package rawbody lets thronectl print the response bodies behind the typed API as Throne
// sent them.
package rawbody

import (
	"context"
	"encoding/json"
)

type key struct{}

// With returns a context in which every successful call stores its response body in dst.
func With(ctx context.Context, dst *json.RawMessage) context.Context {
	return context.WithValue(ctx, key{}, dst)
}

// Store saves body where ctx asks for it, if it does.
func Store(ctx context.Context, body json.RawMessage) {
	if dst, ok := ctx.Value(key{}).(*json.RawMessage); ok {
		*dst = body
	}
}
