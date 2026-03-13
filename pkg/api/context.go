package api

import "context"

type contextKey int

const (
	noCacheKey contextKey = iota
)

// WithNoCache returns a context with the nocache flag set
func WithNoCache(ctx context.Context, noCache bool) context.Context {
	return context.WithValue(ctx, noCacheKey, noCache)
}

// NoCache returns the nocache flag from the context
func NoCache(ctx context.Context) bool {
	v, _ := ctx.Value(noCacheKey).(bool)
	return v
}
