package main

import (
	"context"
	"flag"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseQueryFlags_Defaults(t *testing.T) {
	cfg, err := parseQueryFlags(nil)
	require.NoError(t, err)
	assert.Equal(t, ":50052", cfg.listen)
	assert.Equal(t, "file:///tmp/cloudy-demo?create_dir=true", cfg.url)
	assert.Equal(t, 64*1024*1024, cfg.maxMsgSize)
	assert.Equal(t, 100*time.Millisecond, cfg.pollInterval)
}

func TestParseQueryFlags_Custom(t *testing.T) {
	args := []string{
		"-listen", ":50053",
		"-url", "mem://",
		"-max-msg-size", "1048576",
		"-poll-interval", "50ms",
	}
	cfg, err := parseQueryFlags(args)
	require.NoError(t, err)
	assert.Equal(t, ":50053", cfg.listen)
	assert.Equal(t, "mem://", cfg.url)
	assert.Equal(t, 1048576, cfg.maxMsgSize)
	assert.Equal(t, 50*time.Millisecond, cfg.pollInterval)
}

func TestParseQueryFlags_Validation(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "empty listen",
			args: []string{"-listen", ""},
		},
		{
			name: "empty url",
			args: []string{"-url", ""},
		},
		{
			name: "non-positive max msg size",
			args: []string{"-max-msg-size", "0"},
		},
		{
			name: "negative max msg size",
			args: []string{"-max-msg-size", "-1"},
		},
		{
			name: "non-positive poll interval",
			args: []string{"-poll-interval", "0s"},
		},
		{
			name: "unexpected args",
			args: []string{"extra"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseQueryFlags(tc.args)
			assert.Error(t, err)
		})
	}
}

func TestParseQueryFlags_Help(t *testing.T) {
	_, err := parseQueryFlags([]string{"-help"})
	assert.ErrorIs(t, err, flag.ErrHelp)
}

func TestNewQueryServer_Lifecycle(t *testing.T) {
	cfg := queryConfig{
		listen:       "127.0.0.1:0",
		url:          "mem://",
		maxMsgSize:   1024 * 1024,
		pollInterval: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	srv, err := newQueryServer(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, srv.Addr())

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ctx)
	}()

	cancel()
	err = <-errCh
	assert.NoError(t, err)
}
