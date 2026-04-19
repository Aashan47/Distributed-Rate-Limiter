package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "github.com/Aashan47/Distributed-Rate-Limiter/gen/go/ratelimiter/v1"
	"github.com/Aashan47/Distributed-Rate-Limiter/internal/limiter"
	"github.com/Aashan47/Distributed-Rate-Limiter/internal/metrics"
	"github.com/Aashan47/Distributed-Rate-Limiter/internal/server"
)

// stands up the gRPC server on a real localhost port, then points the gateway at it.
func startFullStack(t *testing.T) (string, func()) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	rules := limiter.NewStaticRules([]limiter.Rule{
		{Name: "api", Limit: 2, Window: time.Minute, Algorithm: "sliding_window_log"},
	})
	lim := limiter.NewSlidingWindowLog(rdb)
	m := metrics.New()
	m.ActiveRules.Set(1)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcAddr := lis.Addr().String()

	grpcSrv := grpc.NewServer()
	pb.RegisterRateLimiterServiceServer(grpcSrv, server.NewRateLimiterServer(lim, rules, m))
	go func() { _ = grpcSrv.Serve(lis) }()

	ctx, cancel := context.WithCancel(context.Background())
	handler, err := server.NewHTTPHandler(ctx, grpcAddr, m.Handler())
	require.NoError(t, err)

	ts := httptest.NewServer(handler)

	cleanup := func() {
		ts.Close()
		cancel()
		grpcSrv.Stop()
		_ = rdb.Close()
	}
	return ts.URL, cleanup
}

func TestREST_AllowAndMetricsFlow(t *testing.T) {
	base, cleanup := startFullStack(t)
	defer cleanup()

	// REST Allow #1: admitted
	resp, err := http.Post(base+"/v1/allow", "application/json",
		strings.NewReader(`{"key":"user:1","rule":"api"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	decision := got["decision"].(map[string]any)
	require.Equal(t, true, decision["allowed"])

	// REST Allow #2: admitted (limit=2)
	resp2, err := http.Post(base+"/v1/allow", "application/json",
		strings.NewReader(`{"key":"user:1","rule":"api"}`))
	require.NoError(t, err)
	resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)

	// REST Allow #3: denied
	resp3, err := http.Post(base+"/v1/allow", "application/json",
		strings.NewReader(`{"key":"user:1","rule":"api"}`))
	require.NoError(t, err)
	defer resp3.Body.Close()
	body, _ := io.ReadAll(resp3.Body)
	require.Contains(t, string(body), `"allowed":false`)

	// Healthz
	hresp, err := http.Get(base + "/healthz")
	require.NoError(t, err)
	hresp.Body.Close()
	require.Equal(t, http.StatusOK, hresp.StatusCode)

	// Metrics surface counters for the three decisions above.
	mresp, err := http.Get(base + "/metrics")
	require.NoError(t, err)
	defer mresp.Body.Close()
	body, _ = io.ReadAll(mresp.Body)
	text := string(body)
	require.Contains(t, text, `ratelimiter_requests_total{decision="allowed",rule="api"} 2`)
	require.Contains(t, text, `ratelimiter_requests_total{decision="denied",rule="api"} 1`)
	require.Contains(t, text, `ratelimiter_active_rules 1`)
}
