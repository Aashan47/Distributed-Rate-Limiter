// Package server implements the gRPC + REST handlers for the rate limiter.
package server

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/Aashan47/Distributed-Rate-Limiter/gen/go/ratelimiter/v1"
	"github.com/Aashan47/Distributed-Rate-Limiter/internal/limiter"
	"github.com/Aashan47/Distributed-Rate-Limiter/internal/metrics"
)

type RateLimiterServer struct {
	pb.UnimplementedRateLimiterServiceServer
	limiter limiter.Limiter
	rules   limiter.RuleRegistry
	metrics *metrics.Metrics
}

func NewRateLimiterServer(l limiter.Limiter, r limiter.RuleRegistry, m *metrics.Metrics) *RateLimiterServer {
	return &RateLimiterServer{limiter: l, rules: r, metrics: m}
}

func (s *RateLimiterServer) observe(rule string, allowed bool, started time.Time, err error) {
	if s.metrics == nil {
		return
	}
	if err != nil {
		s.metrics.RedisErrors.Inc()
		return
	}
	label := metrics.DecisionLabel(allowed)
	s.metrics.Requests.WithLabelValues(rule, label).Inc()
	s.metrics.DecisionLatency.WithLabelValues(rule, label).Observe(time.Since(started).Seconds())
}

func (s *RateLimiterServer) Allow(ctx context.Context, req *pb.AllowRequest) (*pb.AllowResponse, error) {
	if err := validateKeyRule(req.GetKey(), req.GetRule()); err != nil {
		return nil, err
	}
	rule, ok := s.rules.Lookup(req.GetRule())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "rule %q not found", req.GetRule())
	}
	started := time.Now()
	decision, err := s.limiter.Allow(ctx, req.GetKey(), rule)
	s.observe(rule.Name, decision.Allowed, started, err)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "limiter: %v", err)
	}
	return &pb.AllowResponse{Decision: toProtoDecision(decision)}, nil
}

func (s *RateLimiterServer) AllowN(ctx context.Context, req *pb.AllowNRequest) (*pb.AllowNResponse, error) {
	if err := validateKeyRule(req.GetKey(), req.GetRule()); err != nil {
		return nil, err
	}
	rule, ok := s.rules.Lookup(req.GetRule())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "rule %q not found", req.GetRule())
	}
	cost := req.GetCost()
	if cost == 0 {
		cost = 1
	}
	started := time.Now()
	decision, err := s.limiter.AllowN(ctx, req.GetKey(), rule, cost)
	s.observe(rule.Name, decision.Allowed, started, err)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "limiter: %v", err)
	}
	return &pb.AllowNResponse{Decision: toProtoDecision(decision)}, nil
}

func (s *RateLimiterServer) GetRule(ctx context.Context, req *pb.GetRuleRequest) (*pb.GetRuleResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	r, ok := s.rules.Lookup(req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "rule %q not found", req.GetName())
	}
	return &pb.GetRuleResponse{Rule: &pb.Rule{
		Name:      r.Name,
		Limit:     r.Limit,
		Window:    durationpb.New(r.Window),
		Algorithm: r.Algorithm,
	}}, nil
}

func validateKeyRule(key, rule string) error {
	var missing []string
	if key == "" {
		missing = append(missing, "key")
	}
	if rule == "" {
		missing = append(missing, "rule")
	}
	if len(missing) > 0 {
		return status.Errorf(codes.InvalidArgument, "missing required fields: %v", missing)
	}
	return nil
}

func toProtoDecision(d limiter.Decision) *pb.Decision {
	return &pb.Decision{
		Allowed:    d.Allowed,
		Remaining:  d.Remaining,
		ResetAt:    timestamppb.New(d.ResetAt),
		RetryAfter: durationpb.New(d.RetryAfter),
	}
}

// compile-time assertion
var _ pb.RateLimiterServiceServer = (*RateLimiterServer)(nil)
var _ = errors.New
