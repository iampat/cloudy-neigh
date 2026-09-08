package main

import (
	"context"
	"testing"

	"github.com/iampat/cloudy-neigh/logstream"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestParseIngestFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    ingestConfig
		wantErr bool
	}{
		{
			name: "default values",
			args: []string{},
			want: ingestConfig{
				listen:     ":50051",
				url:        "file:///tmp/cloudy-demo?create_dir=true",
				stream:     "wal",
				maxMsgSize: 64 * 1024 * 1024,
			},
		},
		{
			name: "custom values",
			args: []string{
				"-listen", ":50052",
				"-url", "mem://",
				"-stream", "custom-wal",
				"-max-msg-size", "1048576",
			},
			want: ingestConfig{
				listen:     ":50052",
				url:        "mem://",
				stream:     "custom-wal",
				maxMsgSize: 1048576,
			},
		},
		{
			name:    "unknown flag",
			args:    []string{"-unknown-flag"},
			wantErr: true,
		},
		{
			name:    "negative max-msg-size",
			args:    []string{"-max-msg-size", "-1"},
			wantErr: true,
		},
		{
			name:    "zero max-msg-size",
			args:    []string{"-max-msg-size", "0"},
			wantErr: true,
		},
		{
			name:    "empty listen",
			args:    []string{"-listen", ""},
			wantErr: true,
		},
		{
			name:    "empty url",
			args:    []string{"-url", ""},
			wantErr: true,
		},
		{
			name:    "empty stream",
			args:    []string{"-stream", ""},
			wantErr: true,
		},
		{
			name:    "unexpected positional arg",
			args:    []string{"unexpected"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseIngestFlags(tc.args)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRun_InvalidArgs(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "no args",
			args:    nil,
			wantErr: "subcommand required",
		},
		{
			name:    "empty slice",
			args:    []string{},
			wantErr: "subcommand required",
		},
		{
			name:    "query subcommand",
			args:    []string{"query"},
			wantErr: "query engine not yet implemented",
		},
		{
			name:    "query with extra flags",
			args:    []string{"query", "-listen", ":50051"},
			wantErr: "query engine not yet implemented",
		},
		{
			name:    "unknown subcommand",
			args:    []string{"flusher"},
			wantErr: "unknown subcommand \"flusher\"",
		},
		{
			name:    "ingest invalid flag",
			args:    []string{"ingest", "-bogus"},
			wantErr: "flag provided but not defined",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := run(ctx, tc.args)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestNewIngestServer_Errors(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		cfg     ingestConfig
		wantErr string
	}{
		{
			name: "unsupported store url",
			cfg: ingestConfig{
				listen:     "127.0.0.1:0",
				url:        "unsupported://scheme",
				stream:     "wal",
				maxMsgSize: 1024,
			},
			wantErr: "unsupported scheme",
		},
		{
			name: "invalid listen address",
			cfg: ingestConfig{
				listen:     "999.999.999.999:1234",
				url:        "mem://",
				stream:     "wal",
				maxMsgSize: 1024,
			},
			wantErr: "listen on",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, err := newIngestServer(ctx, tc.cfg)
			if srv != nil {
				srv.grpcServer.Stop()
				srv.lis.Close()
				srv.store.Close()
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestIngestServer_EndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := ingestConfig{
		listen:     "127.0.0.1:0",
		url:        "mem://",
		stream:     "wal",
		maxMsgSize: 64 * 1024 * 1024,
	}

	srv, err := newIngestServer(ctx, cfg)
	require.NoError(t, err)

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- srv.Serve(ctx)
	}()

	conn, err := grpc.NewClient(
		srv.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()

	client := cloudyneighpb.NewIngestServiceClient(conn)

	doc := &cloudyneighpb.Document{
		Id:     "doc-test-1",
		Vector: []float32{0.1, 0.2, 0.3},
		Attributes: map[string]string{
			"url":   "https://example.com/1",
			"title": "Test Title",
			"text":  "Sample document text",
			"lang":  "en",
		},
	}

	resp, err := client.Upsert(ctx, &cloudyneighpb.UpsertRequest{
		Namespace: "main",
		Documents: []*cloudyneighpb.Document{doc},
	})
	require.NoError(t, err)
	assert.Equal(t, uint32(1), resp.UpsertedCount)

	log, err := logstream.New(srv.store, cfg.stream)
	require.NoError(t, err)

	tail, err := log.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), tail)

	records, err := log.Read(ctx, 1)
	require.NoError(t, err)
	require.Len(t, records, 1)

	cancel()

	err = <-serverErrCh
	require.NoError(t, err)
}
