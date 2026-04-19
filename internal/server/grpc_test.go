package server_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/Aashan47/Distributed-Rate-Limiter/gen/go/ratelimiter/v1"
	"github.com/Aashan47/Distributed-Rate-Limiter/internal/limiter"
	"github.com/Aashan47/Distributed-Rate-Limiter/internal/server"
)

func startTestServer(t *testing.T) (pb.RateLimiterServiceClient, func()) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	rules := limiter.NewStaticRules([]limiter.Rule{
		{Name: "login", Limit: 3, Window: time.Minute, Algorithm: "sliding_window_log"},
	})
	lim := limiter.NewSlidingWindowLog(rdb)

	lis := bufconn.Listen(1024 * 1024)
	grpcSrv := grpc.NewServer()
	pb.RegisterRateLimiterServiceServer(grpcSrv, server.NewRateLimiterServer(lim, rules, nil))
	go func() { _ = grpcSrv.Serve(lis) }()

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	cleanup := func() {
		_ = conn.Close()
		grpcSrv.Stop()
		_ = rdb.Close()
	}
	return pb.NewRateLimiterServiceClient(conn), cleanup
}

func TestGRPC_Allow_AdmitsUnderLimit(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	for i := 0; i < 3; i++ {
		resp, err := client.Allow(context.Background(), &pb.AllowRequest{Key: "user:1", Rule: "login"})
		require.NoError(t, err)
		require.True(t, resp.GetDecision().GetAllowed())
	}
	resp, err := client.Allow(context.Background(), &pb.AllowRequest{Key: "user:1", Rule: "login"})
	require.NoError(t, err)
	require.False(t, resp.GetDecision().GetAllowed())
	require.Greater(t, resp.GetDecision().GetRetryAfter().AsDuration(), time.Duration(0))
}

func TestGRPC_Allow_UnknownRuleReturnsNotFound(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	_, err := client.Allow(context.Background(), &pb.AllowRequest{Key: "user:1", Rule: "does-not-exist"})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestGRPC_Allow_MissingFieldsRejected(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	_, err := client.Allow(context.Background(), &pb.AllowRequest{Key: "", Rule: "login"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = client.Allow(context.Background(), &pb.AllowRequest{Key: "user:1", Rule: ""})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestGRPC_AllowN_RespectsCost(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	resp, err := client.AllowN(context.Background(), &pb.AllowNRequest{Key: "user:1", Rule: "login", Cost: 2})
	require.NoError(t, err)
	require.True(t, resp.GetDecision().GetAllowed())
	require.EqualValues(t, 1, resp.GetDecision().GetRemaining())

	resp, err = client.AllowN(context.Background(), &pb.AllowNRequest{Key: "user:1", Rule: "login", Cost: 2})
	require.NoError(t, err)
	require.False(t, resp.GetDecision().GetAllowed(), "cost 2 exceeds remaining 1")
}

func TestGRPC_GetRule(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	resp, err := client.GetRule(context.Background(), &pb.GetRuleRequest{Name: "login"})
	require.NoError(t, err)
	require.Equal(t, "login", resp.GetRule().GetName())
	require.EqualValues(t, 3, resp.GetRule().GetLimit())
	require.Equal(t, time.Minute, resp.GetRule().GetWindow().AsDuration())

	_, err = client.GetRule(context.Background(), &pb.GetRuleRequest{Name: "missing"})
	require.Equal(t, codes.NotFound, status.Code(err))
}
