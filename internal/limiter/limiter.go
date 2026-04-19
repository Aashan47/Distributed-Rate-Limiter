// Package limiter defines the rate-limiter contract and its implementations.
package limiter

import (
	"context"
	"time"
)

type Rule struct {
	Name      string
	Limit     uint32
	Window    time.Duration
	Algorithm string
}

type Decision struct {
	Allowed    bool
	Remaining  uint32
	ResetAt    time.Time
	RetryAfter time.Duration
}

type Limiter interface {
	Allow(ctx context.Context, key string, rule Rule) (Decision, error)
	AllowN(ctx context.Context, key string, rule Rule, cost uint32) (Decision, error)
}
