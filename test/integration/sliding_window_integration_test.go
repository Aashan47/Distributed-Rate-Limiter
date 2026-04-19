//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/Aashan47/Distributed-Rate-Limiter/internal/limiter"
)

// redisEndpoint spins up a real redis:7-alpine container via testcontainers and
// returns its host:port. The container is torn down automatically via t.Cleanup.
func redisEndpoint(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	c, err := tcredis.Run(ctx, "redis:7-alpine")
	require.NoError(t, err, "start redis container")
	t.Cleanup(func() {
		_ = testcontainers.TerminateContainer(c)
	})

	host, err := c.Host(ctx)
	require.NoError(t, err)
	port, err := c.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)
	return fmt.Sprintf("%s:%s", host, port.Port())
}

// Against real Redis: hammering one key with 500 goroutines must admit
// exactly `limit` requests — the Lua script provides the atomicity.
func TestIntegration_SlidingWindow_AtomicAdmission(t *testing.T) {
	addr := redisEndpoint(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() { _ = rdb.Close() })

	l := limiter.NewSlidingWindowLog(rdb)
	rule := limiter.Rule{Name: "burst", Limit: 100, Window: time.Minute}

	const workers = 500
	var admitted int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			d, err := l.Allow(context.Background(), "shared", rule)
			if err != nil {
				t.Errorf("unexpected: %v", err)
				return
			}
			if d.Allowed {
				atomic.AddInt64(&admitted, 1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int64(rule.Limit), admitted, "real-redis atomicity under burst")
}

// Window rollover against real Redis — uses wall-clock, so we wait briefly.
func TestIntegration_SlidingWindow_Rollover(t *testing.T) {
	addr := redisEndpoint(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() { _ = rdb.Close() })

	l := limiter.NewSlidingWindowLog(rdb)
	rule := limiter.Rule{Name: "rollover", Limit: 1, Window: 500 * time.Millisecond}

	d, err := l.Allow(context.Background(), "k", rule)
	require.NoError(t, err)
	require.True(t, d.Allowed)

	d, err = l.Allow(context.Background(), "k", rule)
	require.NoError(t, err)
	require.False(t, d.Allowed)

	time.Sleep(600 * time.Millisecond)

	d, err = l.Allow(context.Background(), "k", rule)
	require.NoError(t, err)
	require.True(t, d.Allowed, "should re-admit after window expires against real Redis")
}
