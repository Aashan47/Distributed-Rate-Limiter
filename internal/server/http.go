package server

import (
	"context"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/Aashan47/Distributed-Rate-Limiter/gen/go/ratelimiter/v1"
)

// NewHTTPHandler builds an http.Handler that serves the REST gateway plus
// health and metrics endpoints. The gateway forwards to the gRPC server at
// grpcAddr over loopback, so the caller must have that server running.
func NewHTTPHandler(ctx context.Context, grpcAddr string, metrics http.Handler) (http.Handler, error) {
	gwMux := runtime.NewServeMux()
	dialOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if err := pb.RegisterRateLimiterServiceHandlerFromEndpoint(ctx, gwMux, grpcAddr, dialOpts); err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("/v1/", gwMux)
	mux.HandleFunc("/healthz", okHandler)
	mux.HandleFunc("/readyz", okHandler)
	if metrics != nil {
		mux.Handle("/metrics", metrics)
	}
	return mux, nil
}

func okHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
