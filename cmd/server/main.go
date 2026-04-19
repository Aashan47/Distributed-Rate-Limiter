package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	pb "github.com/Aashan47/Distributed-Rate-Limiter/gen/go/ratelimiter/v1"
	"github.com/Aashan47/Distributed-Rate-Limiter/internal/config"
	"github.com/Aashan47/Distributed-Rate-Limiter/internal/limiter"
	"github.com/Aashan47/Distributed-Rate-Limiter/internal/metrics"
	"github.com/Aashan47/Distributed-Rate-Limiter/internal/server"
)

func main() {
	cfgPath := flag.String("config", "config/rules.yaml", "path to YAML config")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(*cfgPath); err != nil {
		slog.Error("server exited with error", "err", err)
		os.Exit(1)
	}
}

func run(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	defer rdb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := rdb.Ping(ctx).Err(); err != nil {
		cancel()
		return fmt.Errorf("redis ping: %w", err)
	}
	cancel()
	slog.Info("redis connected", "addr", cfg.Redis.Addr)

	rules := limiter.NewStaticRules(cfg.ToLimiterRules())
	lim := limiter.NewSlidingWindowLog(rdb)

	m := metrics.New()
	m.ActiveRules.Set(float64(len(cfg.Rules)))

	grpcSrv := grpc.NewServer()
	pb.RegisterRateLimiterServiceServer(grpcSrv, server.NewRateLimiterServer(lim, rules, m))
	reflection.Register(grpcSrv)

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// gRPC listener
	lis, err := net.Listen("tcp", cfg.Server.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen grpc: %w", err)
	}

	grpcErr := make(chan error, 1)
	go func() {
		slog.Info("grpc server listening", "addr", cfg.Server.GRPCAddr)
		grpcErr <- grpcSrv.Serve(lis)
	}()

	// HTTP gateway
	httpHandler, err := server.NewHTTPHandler(rootCtx, cfg.Server.GRPCAddr, m.Handler())
	if err != nil {
		grpcSrv.GracefulStop()
		return fmt.Errorf("http handler: %w", err)
	}
	httpSrv := &http.Server{
		Addr:              cfg.Server.HTTPAddr,
		Handler:           httpHandler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	httpErr := make(chan error, 1)
	go func() {
		slog.Info("http server listening", "addr", cfg.Server.HTTPAddr)
		httpErr <- httpSrv.ListenAndServe()
	}()

	// Wait for shutdown signal or a server failure.
	select {
	case <-rootCtx.Done():
		slog.Info("shutdown signal received")
	case err := <-grpcErr:
		if err != nil {
			slog.Error("grpc serve error", "err", err)
		}
	case err := <-httpErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http serve error", "err", err)
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http shutdown", "err", err)
	}
	grpcSrv.GracefulStop()
	slog.Info("server stopped")
	return nil
}
