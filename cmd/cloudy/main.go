package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/iampat/cloudy-neigh/grpcapi"
	"github.com/iampat/cloudy-neigh/ingest"
	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
)

type ingestConfig struct {
	listen        string
	url           string
	stream        string
	maxMsgSize    int
	flushDocs     int
	flushInterval time.Duration
	pollInterval  time.Duration
}

func parseIngestFlags(args []string) (ingestConfig, error) {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)

	var cfg ingestConfig
	fs.StringVar(&cfg.listen, "listen", ":50051", "address:port to listen on")
	fs.StringVar(&cfg.url, "url", "file:///tmp/cloudy-demo?create_dir=true", "object storage URL")
	fs.StringVar(&cfg.stream, "stream", "wal", "WAL stream name")
	fs.IntVar(&cfg.maxMsgSize, "max-msg-size", 64*1024*1024, "maximum message size in bytes")
	fs.IntVar(&cfg.flushDocs, "flush-docs", 10000, "memtable doc threshold for flush")
	fs.DurationVar(&cfg.flushInterval, "flush-interval", 10*time.Second, "memtable time threshold for flush")
	fs.DurationVar(&cfg.pollInterval, "poll-interval", 100*time.Millisecond, "WAL poll interval")

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
	if cfg.flushDocs <= 0 {
		return ingestConfig{}, errors.New("-flush-docs must be positive")
	}
	if cfg.flushInterval <= 0 {
		return ingestConfig{}, errors.New("-flush-interval must be positive")
	}
	if cfg.pollInterval <= 0 {
		return ingestConfig{}, errors.New("-poll-interval must be positive")
	}
	return cfg, nil
}

type ingestServer struct {
	lis        net.Listener
	grpcServer *grpc.Server
	store      objectstore.Store
	flusher    *ingest.Flusher
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

	flusher, err := ingest.NewFlusher(store, log, ingest.Config{
		DocThreshold:  cfg.flushDocs,
		TimeThreshold: cfg.flushInterval,
		PollInterval:  cfg.pollInterval,
	})
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("create flusher: %w", err)
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
		flusher:    flusher,
	}, nil
}

func (s *ingestServer) Addr() net.Addr {
	return s.lis.Addr()
}

func (s *ingestServer) Serve(ctx context.Context) (err error) {
	defer func() {
		if closeErr := s.store.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	flusherCtx, cancelFlusher := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelFlusher()

	var g errgroup.Group

	g.Go(func() error {
		err := serveGRPC(ctx, s.grpcServer, s.lis)
		cancelFlusher()
		return err
	})

	g.Go(func() error {
		err := s.flusher.Run(flusherCtx)
		s.grpcServer.Stop()
		return err
	})

	return g.Wait()
}

func serveGRPC(ctx context.Context, srv *grpc.Server, lis net.Listener) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(lis)
	}()

	select {
	case <-ctx.Done():
		stopped := make(chan struct{})
		go func() {
			srv.GracefulStop()
			close(stopped)
		}()

		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			srv.Stop()
			<-stopped
		}

		err := <-errCh
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return err
	case err := <-errCh:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return err
	}
}

func runIngest(ctx context.Context, args []string) error {
	cfg, err := parseIngestFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	srv, err := newIngestServer(ctx, cfg)
	if err != nil {
		return err
	}
	slog.Info("ingest server listening", "addr", srv.Addr().String(), "stream", cfg.stream, "url", cfg.url)

	return srv.Serve(ctx)
}

type queryConfig struct {
	listen     string
	maxMsgSize int
}

func parseQueryFlags(args []string) (queryConfig, error) {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)

	var cfg queryConfig
	fs.StringVar(&cfg.listen, "listen", ":50052", "address:port to listen on")
	fs.IntVar(&cfg.maxMsgSize, "max-msg-size", 64*1024*1024, "maximum message size in bytes")

	if err := fs.Parse(args); err != nil {
		return queryConfig{}, err
	}
	if len(fs.Args()) > 0 {
		return queryConfig{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if cfg.listen == "" {
		return queryConfig{}, errors.New("-listen cannot be empty")
	}
	if cfg.maxMsgSize <= 0 {
		return queryConfig{}, errors.New("-max-msg-size must be positive")
	}
	return cfg, nil
}

type queryServer struct {
	lis        net.Listener
	grpcServer *grpc.Server
}

func newQueryServer(cfg queryConfig) (*queryServer, error) {
	lis, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", cfg.listen, err)
	}

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(cfg.maxMsgSize),
		grpc.MaxSendMsgSize(cfg.maxMsgSize),
	)
	cloudyneighpb.RegisterQueryServiceServer(grpcServer, new(grpcapi.QueryServer))

	return &queryServer{
		lis:        lis,
		grpcServer: grpcServer,
	}, nil
}

func (s *queryServer) Addr() net.Addr {
	return s.lis.Addr()
}

func (s *queryServer) Serve(ctx context.Context) error {
	return serveGRPC(ctx, s.grpcServer, s.lis)
}

func runQuery(ctx context.Context, args []string) error {
	cfg, err := parseQueryFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	srv, err := newQueryServer(cfg)
	if err != nil {
		return err
	}
	slog.Info("query server listening", "addr", srv.Addr().String())

	return srv.Serve(ctx)
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: cloudy <subcommand> [flags]\n\nSubcommands:\n  ingest    run ingest gRPC service\n  query     run query service\n")
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("subcommand required: ingest, query")
	}
	if args[0] == "-h" || args[0] == "-help" || args[0] == "--help" {
		usage()
		return nil
	}

	switch args[0] {
	case "ingest":
		return runIngest(ctx, args[1:])
	case "query":
		return runQuery(ctx, args[1:])
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		log.Fatalf("cloudy: %v", err)
	}
}
