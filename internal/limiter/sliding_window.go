package limiter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type SlidingWindowLog struct {
	rdb    redis.Scripter
	script *redis.Script
	prefix string
	now    func() time.Time
}

type SlidingWindowOption func(*SlidingWindowLog)

func WithPrefix(prefix string) SlidingWindowOption {
	return func(s *SlidingWindowLog) { s.prefix = prefix }
}

// WithClock lets tests inject a deterministic clock.
func WithClock(nowFn func() time.Time) SlidingWindowOption {
	return func(s *SlidingWindowLog) { s.now = nowFn }
}

func NewSlidingWindowLog(rdb redis.Scripter, opts ...SlidingWindowOption) *SlidingWindowLog {
	s := &SlidingWindowLog{
		rdb:    rdb,
		script: redis.NewScript(slidingWindowScript),
		prefix: "rl:",
		now:    time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *SlidingWindowLog) Allow(ctx context.Context, key string, rule Rule) (Decision, error) {
	return s.AllowN(ctx, key, rule, 1)
}

func (s *SlidingWindowLog) AllowN(ctx context.Context, key string, rule Rule, cost uint32) (Decision, error) {
	if rule.Limit == 0 {
		return Decision{}, errors.New("limiter: rule.Limit must be > 0")
	}
	if rule.Window <= 0 {
		return Decision{}, errors.New("limiter: rule.Window must be > 0")
	}
	if cost == 0 {
		cost = 1
	}
	if uint32(cost) > rule.Limit {
		// Structurally impossible; fail fast instead of calling Redis.
		now := s.now()
		return Decision{
			Allowed:    false,
			Remaining:  0,
			ResetAt:    now.Add(rule.Window),
			RetryAfter: rule.Window,
		}, nil
	}

	now := s.now()
	nowMs := now.UnixMilli()
	windowMs := rule.Window.Milliseconds()
	reqID := uuid.NewString()

	fullKey := s.prefix + rule.Name + ":" + key

	raw, err := s.script.Run(ctx, s.rdb, []string{fullKey},
		nowMs, windowMs, rule.Limit, cost, reqID).Result()
	if err != nil {
		return Decision{}, fmt.Errorf("limiter: script run: %w", err)
	}

	arr, ok := raw.([]interface{})
	if !ok || len(arr) != 4 {
		return Decision{}, fmt.Errorf("limiter: unexpected script response %T: %v", raw, raw)
	}

	allowed, err := coerceInt64(arr[0])
	if err != nil {
		return Decision{}, fmt.Errorf("limiter: allowed field: %w", err)
	}
	remaining, err := coerceInt64(arr[1])
	if err != nil {
		return Decision{}, fmt.Errorf("limiter: remaining field: %w", err)
	}
	resetAtMs, err := coerceInt64(arr[2])
	if err != nil {
		return Decision{}, fmt.Errorf("limiter: reset_at field: %w", err)
	}
	retryAfterMs, err := coerceInt64(arr[3])
	if err != nil {
		return Decision{}, fmt.Errorf("limiter: retry_after field: %w", err)
	}

	return Decision{
		Allowed:    allowed == 1,
		Remaining:  uint32(remaining),
		ResetAt:    time.UnixMilli(resetAtMs),
		RetryAfter: time.Duration(retryAfterMs) * time.Millisecond,
	}, nil
}

func coerceInt64(v interface{}) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case int:
		return int64(x), nil
	case int32:
		return int64(x), nil
	case float64:
		return int64(x), nil
	case string:
		var n int64
		_, err := fmt.Sscanf(x, "%d", &n)
		return n, err
	default:
		return 0, fmt.Errorf("cannot coerce %T to int64", v)
	}
}

var _ Limiter = (*SlidingWindowLog)(nil)
