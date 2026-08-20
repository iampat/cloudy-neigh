package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/iampat/cloudy-neigh/cas"
	"github.com/iampat/cloudy-neigh/index"
	"github.com/iampat/cloudy-neigh/proto/cloudyneigh"
	"github.com/iampat/cloudy-neigh/server"
)

func newServeCommand() *cobra.Command {
	var addr, store, dir string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the Index service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serve(cmd.Context(), addr, store, resolvePath(dir))
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "listen address")
	cmd.Flags().StringVar(&store, "store", "memory", "storage backend: memory or disk")
	cmd.Flags().StringVar(&dir, "dir", "", "data directory, required when store is disk")
	return cmd
}

func serve(ctx context.Context, addr, store, dir string) error {
	blobs, err := openStore(store, dir)
	if err != nil {
		return err
	}
	idx, err := index.Open(blobs)
	if err != nil {
		return fmt.Errorf("open index: %w", err)
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	srv := grpc.NewServer()
	cloudyneigh.RegisterIndexAPIServer(srv, server.New(idx))
	reflection.Register(srv)

	go func() {
		<-ctx.Done()
		slog.Info("shutting down")
		srv.GracefulStop()
	}()

	slog.Info("serving", "addr", listener.Addr().String(), "store", store)
	if err := srv.Serve(listener); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func openStore(kind, dir string) (cas.Store, error) {
	switch kind {
	case "memory":
		if dir != "" {
			return nil, errors.New("--dir applies to the disk store only")
		}
		return cas.NewMemory(), nil
	case "disk":
		if dir == "" {
			return nil, errors.New("--dir is required when store is disk")
		}
		return cas.OpenDisk(dir)
	default:
		return nil, fmt.Errorf("unknown store %q, want memory or disk", kind)
	}
}
