package limiter_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/Aashan47/Distributed-Rate-Limiter/internal/limiter"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func setup(t *testing.T) (*limiter.SlidingWindowLog, *miniredis.Miniredis, *fakeClock) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	clock := newFakeClock(time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC))
	l := limiter.NewSlidingWindowLog(rdb, limiter.WithClock(clock.Now))
	return l, mr, clock
}

func TestSlidingWindow_UnderLimit(t *testing.T) {
	l, _, _ := setup(t)
	rule := limiter.Rule{Name: "api", Limit: 5, Window: time.Minute}

	for i := 0; i < 5; i++ {
		d, err := l.Allow(context.Background(), "user:1", rule)
		require.NoError(t, err)
		require.True(t, d.Allowed, "request %d should be allowed", i)
		require.Equal(t, uint32(4-i), d.Remaining)
	}
}

func TestSlidingWindow_RejectsOverLimit(t *testing.T) {
	l, _, _ := setup(t)
	rule := limiter.Rule{Name: "api", Limit: 3, Window: time.Minute}

	for i := 0; i < 3; i++ {
		d, err := l.Allow(context.Background(), "user:1", rule)
		require.NoError(t, err)
		require.True(t, d.Allowed)
	}

	d, err := l.Allow(context.Background(), "user:1", rule)
	require.NoError(t, err)
	require.False(t, d.Allowed, "4th request must be rejected")
	require.Equal(t, uint32(0), d.Remaining)
	require.Greater(t, d.RetryAfter, time.Duration(0))
}

func TestSlidingWindow_WindowRollover(t *testing.T) {
	l, _, clock := setup(t)
	rule := limiter.Rule{Name: "api", Limit: 2, Window: time.Minute}

	d, err := l.Allow(context.Background(), "user:1", rule)
	require.NoError(t, err)
	require.True(t, d.Allowed)

	d, err = l.Allow(context.Background(), "user:1", rule)
	require.NoError(t, err)
	require.True(t, d.Allowed)

	d, err = l.Allow(context.Background(), "user:1", rule)
	require.NoError(t, err)
	require.False(t, d.Allowed)

	// Advance past the window; the first two entries should be pruned.
	clock.Advance(61 * time.Second)

	d, err = l.Allow(context.Background(), "user:1", rule)
	require.NoError(t, err)
	require.True(t, d.Allowed, "should re-admit after window rollover")
	require.Equal(t, uint32(1), d.Remaining)
}

func TestSlidingWindow_SeparateKeysIsolated(t *testing.T) {
	l, _, _ := setup(t)
	rule := limiter.Rule{Name: "api", Limit: 1, Window: time.Minute}

	d, err := l.Allow(context.Background(), "user:1", rule)
	require.NoError(t, err)
	require.True(t, d.Allowed)

	d, err = l.Allow(context.Background(), "user:2", rule)
	require.NoError(t, err)
	require.True(t, d.Allowed, "different key must have independent bucket")

	d, err = l.Allow(context.Background(), "user:1", rule)
	require.NoError(t, err)
	require.False(t, d.Allowed)
}

func TestSlidingWindow_AllowN_ConsumesCost(t *testing.T) {
	l, _, _ := setup(t)
	rule := limiter.Rule{Name: "api", Limit: 10, Window: time.Minute}

	d, err := l.AllowN(context.Background(), "user:1", rule, 3)
	require.NoError(t, err)
	require.True(t, d.Allowed)
	require.Equal(t, uint32(7), d.Remaining)

	d, err = l.AllowN(context.Background(), "user:1", rule, 8)
	require.NoError(t, err)
	require.False(t, d.Allowed, "cost exceeds remaining")
}

func TestSlidingWindow_CostExceedsLimit(t *testing.T) {
	l, _, _ := setup(t)
	rule := limiter.Rule{Name: "api", Limit: 5, Window: time.Minute}

	d, err := l.AllowN(context.Background(), "user:1", rule, 6)
	require.NoError(t, err)
	require.False(t, d.Allowed, "cost > limit can never be admitted")
}

func TestSlidingWindow_InvalidRule(t *testing.T) {
	l, _, _ := setup(t)

	_, err := l.Allow(context.Background(), "k", limiter.Rule{Name: "x", Limit: 0, Window: time.Minute})
	require.Error(t, err)

	_, err = l.Allow(context.Background(), "k", limiter.Rule{Name: "x", Limit: 1, Window: 0})
	require.Error(t, err)
}

// Concurrent safety: N goroutines hammer the same key; admitted count must not exceed Limit.
func TestSlidingWindow_ConcurrentSafety(t *testing.T) {
	l, _, _ := setup(t)
	rule := limiter.Rule{Name: "burst", Limit: 100, Window: time.Minute}

	const goroutines = 50
	const perG = 20
	var admitted int64

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				d, err := l.Allow(context.Background(), "shared", rule)
				if err != nil {
					t.Errorf("unexpected error: %v", err)
					return
				}
				if d.Allowed {
					atomic.AddInt64(&admitted, 1)
				}
			}
		}()
	}
	wg.Wait()

	// Total attempts: 1000, Limit: 100. Exactly 100 should have been admitted.
	require.Equal(t, int64(rule.Limit), admitted,
		"admitted count must equal limit — race condition if not")
}

func TestSlidingWindow_RetryAfterDecreasesAsTimePasses(t *testing.T) {
	l, _, clock := setup(t)
	rule := limiter.Rule{Name: "api", Limit: 1, Window: 10 * time.Second}

	_, err := l.Allow(context.Background(), "user:1", rule)
	require.NoError(t, err)

	d, err := l.Allow(context.Background(), "user:1", rule)
	require.NoError(t, err)
	require.False(t, d.Allowed)
	initialRetry := d.RetryAfter

	clock.Advance(3 * time.Second)

	d, err = l.Allow(context.Background(), "user:1", rule)
	require.NoError(t, err)
	require.False(t, d.Allowed)
	require.Less(t, d.RetryAfter, initialRetry, "retry_after should shrink as time advances")
}

// Ensure the key naming scheme produces distinct keys per rule.
func TestSlidingWindow_PerRuleIsolation(t *testing.T) {
	l, _, _ := setup(t)
	ruleA := limiter.Rule{Name: "login", Limit: 1, Window: time.Minute}
	ruleB := limiter.Rule{Name: "read", Limit: 1, Window: time.Minute}

	d, err := l.Allow(context.Background(), "user:1", ruleA)
	require.NoError(t, err)
	require.True(t, d.Allowed)

	d, err = l.Allow(context.Background(), "user:1", ruleB)
	require.NoError(t, err)
	require.True(t, d.Allowed, "different rules for same key must not share bucket")
}

// Benchmark Allow throughput against miniredis to detect regressions (~informational only).
func BenchmarkSlidingWindow_Allow(b *testing.B) {
	mr := miniredis.RunT(b)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	l := limiter.NewSlidingWindowLog(rdb)
	rule := limiter.Rule{Name: "bench", Limit: uint32(b.N + 10), Window: time.Hour}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = l.Allow(context.Background(), fmt.Sprintf("user:%d", i), rule)
	}
}
