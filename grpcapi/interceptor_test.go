package grpcapi_test

import (
	"context"
	"testing"

	"github.com/iampat/cloudy-neigh/grpcapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestTenantInterceptor(t *testing.T) {
	t.Run("missing tenant header", func(t *testing.T) {
		ctx := context.Background()
		_, err := grpcapi.TenantInterceptor(ctx, nil, nil, func(ctx context.Context, req any) (any, error) {
			return nil, nil
		})
		require.Error(t, err)
		st, ok := status.FromError(err)
		require.True(t, ok)
		assert.Equal(t, codes.Unauthenticated, st.Code())
	})

	t.Run("empty tenant header", func(t *testing.T) {
		md := metadata.Pairs(grpcapi.TenantHeader, "")
		ctx := metadata.NewIncomingContext(context.Background(), md)
		_, err := grpcapi.TenantInterceptor(ctx, nil, nil, func(ctx context.Context, req any) (any, error) {
			return nil, nil
		})
		require.Error(t, err)
		st, ok := status.FromError(err)
		require.True(t, ok)
		assert.Equal(t, codes.Unauthenticated, st.Code())
	})

	t.Run("invalid tenant name", func(t *testing.T) {
		md := metadata.Pairs(grpcapi.TenantHeader, "bad/tenant")
		ctx := metadata.NewIncomingContext(context.Background(), md)
		_, err := grpcapi.TenantInterceptor(ctx, nil, nil, func(ctx context.Context, req any) (any, error) {
			return nil, nil
		})
		require.Error(t, err)
		st, ok := status.FromError(err)
		require.True(t, ok)
		assert.Equal(t, codes.InvalidArgument, st.Code())
	})

	t.Run("valid tenant header", func(t *testing.T) {
		md := metadata.Pairs(grpcapi.TenantHeader, "acme")
		ctx := metadata.NewIncomingContext(context.Background(), md)
		var capturedTenant string
		_, err := grpcapi.TenantInterceptor(ctx, nil, nil, func(ctx context.Context, req any) (any, error) {
			capturedTenant = grpcapi.TenantFrom(ctx)
			return "ok", nil
		})
		require.NoError(t, err)
		assert.Equal(t, "acme", capturedTenant)
	})
}
