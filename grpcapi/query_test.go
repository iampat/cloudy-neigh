package grpcapi_test

import (
	"context"
	"net"
	"testing"

	"github.com/iampat/cloudy-neigh/grpcapi"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func setupQueryTestEnv(t *testing.T) cloudyneighpb.QueryServiceClient {
	t.Helper()
	srv := new(grpcapi.QueryServer)

	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	cloudyneighpb.RegisterQueryServiceServer(s, srv)

	go func() {
		_ = s.Serve(lis)
	}()
	t.Cleanup(func() {
		s.GracefulStop()
		lis.Close()
	})

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return cloudyneighpb.NewQueryServiceClient(conn)
}

func TestQuery_Success(t *testing.T) {
	client := setupQueryTestEnv(t)
	ctx := context.Background()

	resp, err := client.Query(ctx, &cloudyneighpb.QueryRequest{
		Namespace: "main",
		Vector:    []float32{0.1, 0.2, 0.3},
		TopK:      10,
	})
	require.Error(t, err)
	assert.Nil(t, resp)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unimplemented, st.Code())
}

func TestQuery_Validation(t *testing.T) {
	client := setupQueryTestEnv(t)
	ctx := context.Background()

	tests := []struct {
		name string
		req  *cloudyneighpb.QueryRequest
	}{
		{
			name: "empty namespace",
			req: &cloudyneighpb.QueryRequest{
				Namespace: "",
			},
		},
		{
			name: "invalid namespace starting with digit",
			req: &cloudyneighpb.QueryRequest{
				Namespace: "123branch",
			},
		},
		{
			name: "invalid namespace with slash",
			req: &cloudyneighpb.QueryRequest{
				Namespace: "feature/branch",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.Query(ctx, tc.req)
			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, codes.InvalidArgument, st.Code())
		})
	}
}

func TestQuery_NilRequest(t *testing.T) {
	srv := new(grpcapi.QueryServer)
	_, err := srv.Query(context.Background(), nil)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}
