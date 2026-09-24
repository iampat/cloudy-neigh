package grpcapi

import (
	"context"

	"github.com/iampat/cloudy-neigh/namespace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type tenantKey struct{}

const TenantHeader = "x-tenant-id"

func TenantInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get(TenantHeader)
	if len(vals) == 0 || vals[0] == "" {
		return nil, status.Error(codes.Unauthenticated, "missing tenant")
	}
	if len(vals) > 1 {
		return nil, status.Error(codes.InvalidArgument, "multiple tenant headers")
	}
	tenant := vals[0]
	if err := namespace.ValidateName(tenant); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid tenant: %v", err)
	}
	return handler(withTenant(ctx, tenant), req)
}

func withTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, tenantKey{}, tenant)
}

func TenantFrom(ctx context.Context) string {
	if val, ok := ctx.Value(tenantKey{}).(string); ok {
		return val
	}
	return ""
}
