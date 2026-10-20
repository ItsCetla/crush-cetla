package message

import "context"

type steerKey struct{}

// WithSteer marks a prompt that should join the session's running turn
// instead of waiting for it to finish.
func WithSteer(ctx context.Context) context.Context {
	return context.WithValue(ctx, steerKey{}, true)
}

// Steer reports whether the caller asked to steer the running turn.
func Steer(ctx context.Context) bool {
	steer, _ := ctx.Value(steerKey{}).(bool)
	return steer
}
