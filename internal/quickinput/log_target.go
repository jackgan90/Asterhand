package quickinput

import "context"

type logRunKey struct{}

// WithLogRun selects one retained instance when explicitly opening its window.
// The caller must validate ownership against the command before opening.
func WithLogRun(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, logRunKey{}, runID)
}
func logRunTarget(ctx context.Context) string { v, _ := ctx.Value(logRunKey{}).(string); return v }
