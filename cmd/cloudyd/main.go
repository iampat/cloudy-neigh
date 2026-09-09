package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/iampat/cloudy-neigh/grpcapi"
	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"google.golang.org/grpc"
)

type ingestConfig struct {
	listen     string
	url        string
	stream     string
	maxMsgSize int
}

func parseIngestFlags(args []string) (ingestConfig, error) {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var cfg ingestConfig
	fs.StringVar(&cfg.listen, "listen", ":50051", "address:port to listen on")
	fs.StringVar(&cfg.url, "url", "file:///tmp/cloudy-demo?create_dir=true", "object storage URL")
	fs.StringVar(&cfg.stream, "stream", "wal", "WAL stream name")
	fs.IntVar(&cfg.maxMsgSize, "max-msg-size", 64*1024*1024, "maximum message size in bytes")

	if err := fs.Parse(args); err != nil {
		return ingestConfig{}, err
	}
	if len(fs.Args()) > 0 {
		return ingestConfig{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if cfg.listen == "" {
		return ingestConfig{}, errors.New("-listen cannot be empty")
	}
	if cfg.url == "" {
		return ingestConfig{}, errors.New("-url cannot be empty")
	}
	if cfg.stream == "" {
		return ingestConfig{}, errors.New("-stream cannot be empty")
	}
	if cfg.maxMsgSize <= 0 {
		return ingestConfig{}, errors.New("-max-msg-size must be positive")
	}
	return cfg, nil
}

type ingestServer struct {
	lis        net.Listener
	grpcServer *grpc.Server
	store      objectstore.Store
}

func newIngestServer(ctx context.Context, cfg ingestConfig) (*ingestServer, error) {
	store, err := objectstore.Open(ctx, cfg.url)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	log, err := logstream.New(store, cfg.stream)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("open logstream: %w", err)
	}

	srv, err := grpcapi.NewIngestServer(log)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("create ingest server: %w", err)
	}

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(cfg.maxMsgSize),
		grpc.MaxSendMsgSize(cfg.maxMsgSize),
	)
	cloudyneighpb.RegisterIngestServiceServer(grpcServer, srv)

	lis, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("listen on %s: %w", cfg.listen, err)
	}

	return &ingestServer{
		lis:        lis,
		grpcServer: grpcServer,
		store:      store,
	}, nil
}

func (s *ingestServer) Addr() net.Addr {
	return s.lis.Addr()
}

func (s *ingestServer) Serve(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.grpcServer.Serve(s.lis)
	}()

	select {
	case <-ctx.Done():
		s.grpcServer.GracefulStop()
		s.store.Close()
		<-errCh
		return nil
	case err := <-errCh:
		s.store.Close()
		return err
	}
}

func runIngest(ctx context.Context, args []string) error {
	cfg, err := parseIngestFlags(args)
	if err != nil {
		return err
	}

	srv, err := newIngestServer(ctx, cfg)
	if err != nil {
		return err
	}
	slog.Info("ingest server listening", "addr", srv.Addr().String(), "stream", cfg.stream, "url", cfg.url)

	return srv.Serve(ctx)
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("subcommand required: ingest, query")
	}

	switch args[0] {
	case "ingest":
		return runIngest(ctx, args[1:])
	case "query":
		return errors.New("query engine not yet implemented")
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		log.Fatalf("cloudyd: %v", err)
	}
}
