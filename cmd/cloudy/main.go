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
	"github.com/iampat/cloudy-neigh/query"
	"github.com/iampat/cloudy-neigh/query/distance"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
)

const (
	maxMsgSize = 64 * 1024 * 1024
	walStream  = "wal"
)

type ingestConfig struct {
	addr         string
	url          string
	pollInterval time.Duration
	debug        bool
}

func parseIngestFlags(args []string) (ingestConfig, error) {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)

	var cfg ingestConfig
	fs.StringVar(&cfg.addr, "addr", ":50051", "address:port to listen on")
	fs.StringVar(&cfg.url, "url", "file:///tmp/cloudy-demo?create_dir=true", "object storage URL")
	fs.DurationVar(&cfg.pollInterval, "poll-interval", 100*time.Millisecond, "WAL poll interval")
	fs.BoolVar(&cfg.debug, "debug", false, "enable debug logging")

	if err := fs.Parse(args); err != nil {
		return ingestConfig{}, err
	}
	if len(fs.Args()) > 0 {
		return ingestConfig{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if cfg.addr == "" {
		return ingestConfig{}, errors.New("-addr cannot be empty")
	}
	if cfg.url == "" {
		return ingestConfig{}, errors.New("-url cannot be empty")
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

	log, err := logstream.New(store, walStream)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("open logstream: %w", err)
	}

	ingester, err := ingest.NewIngester(store, log)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("create ingester: %w", err)
	}

	srv, err := grpcapi.NewIngestServer(ingester)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("create ingest server: %w", err)
	}

	flusher, err := ingest.NewFlusher(store, log, ingest.Config{
		PollInterval: cfg.pollInterval,
	})
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("create flusher: %w", err)
	}

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMsgSize),
		grpc.MaxSendMsgSize(maxMsgSize),
	)
	cloudyneighpb.RegisterIngestServiceServer(grpcServer, srv)

	lis, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("listen on %s: %w", cfg.addr, err)
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

func setupLogging(debug bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	slog.SetLogLoggerLevel(level)
}

func runIngest(ctx context.Context, args []string) error {
	cfg, err := parseIngestFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	setupLogging(cfg.debug)

	srv, err := newIngestServer(ctx, cfg)
	if err != nil {
		return err
	}
	slog.Info("ingest server listening", "addr", srv.Addr().String(), "wal", walStream, "url", cfg.url)

	return srv.Serve(ctx)
}

type queryConfig struct {
	addr         string
	url          string
	syncInterval time.Duration
	debug        bool
}

func parseQueryFlags(args []string) (queryConfig, error) {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)

	var cfg queryConfig
	fs.StringVar(&cfg.addr, "addr", ":50052", "address:port to listen on")
	fs.StringVar(&cfg.url, "url", "file:///tmp/cloudy-demo?create_dir=true", "object storage URL")
	fs.DurationVar(&cfg.syncInterval, "sync-interval", 2*time.Second, "background sync interval")
	fs.BoolVar(&cfg.debug, "debug", false, "enable debug logging")

	if err := fs.Parse(args); err != nil {
		return queryConfig{}, err
	}
	if len(fs.Args()) > 0 {
		return queryConfig{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if cfg.addr == "" {
		return queryConfig{}, errors.New("-addr cannot be empty")
	}
	if cfg.url == "" {
		return queryConfig{}, errors.New("-url cannot be empty")
	}
	if cfg.syncInterval <= 0 {
		return queryConfig{}, errors.New("-sync-interval must be positive")
	}
	return cfg, nil
}

type queryServer struct {
	lis        net.Listener
	grpcServer *grpc.Server
	store      objectstore.Store
	engine     *query.Engine
}

func newQueryServer(ctx context.Context, cfg queryConfig) (*queryServer, error) {
	store, err := objectstore.Open(ctx, cfg.url)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	engine, err := query.NewEngine(store, cfg.syncInterval)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("create query engine: %w", err)
	}

	srv, err := grpcapi.NewQueryServer(engine)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("create query server: %w", err)
	}

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMsgSize),
		grpc.MaxSendMsgSize(maxMsgSize),
	)
	cloudyneighpb.RegisterQueryServiceServer(grpcServer, srv)

	lis, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("listen on %s: %w", cfg.addr, err)
	}

	return &queryServer{
		lis:        lis,
		grpcServer: grpcServer,
		store:      store,
		engine:     engine,
	}, nil
}

func (s *queryServer) Addr() net.Addr {
	return s.lis.Addr()
}

func (s *queryServer) Serve(ctx context.Context) (err error) {
	defer func() {
		if closeErr := s.store.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	syncCtx, cancelSync := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelSync()

	var g errgroup.Group

	g.Go(func() error {
		err := serveGRPC(ctx, s.grpcServer, s.lis)
		cancelSync()
		return err
	})

	g.Go(func() error {
		err := s.engine.Run(syncCtx)
		s.grpcServer.Stop()
		return err
	})

	return g.Wait()
}

func runQuery(ctx context.Context, args []string) error {
	cfg, err := parseQueryFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	setupLogging(cfg.debug)

	srv, err := newQueryServer(ctx, cfg)
	if err != nil {
		return err
	}
	slog.Info("query server listening", "addr", srv.Addr().String(), "url", cfg.url, "kernel", distance.Implementation())

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
