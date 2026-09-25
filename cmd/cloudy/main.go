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
	"github.com/iampat/cloudy-neigh/namespace"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"github.com/iampat/cloudy-neigh/query"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
)

const maxMsgSize = 64 * 1024 * 1024

type ingestConfig struct {
	addr         string
	tenantsFile  string
	pollInterval time.Duration
	debug        bool
}

func parseIngestFlags(args []string) (ingestConfig, error) {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)

	var cfg ingestConfig
	fs.StringVar(&cfg.addr, "addr", ":50051", "address:port to listen on")
	fs.StringVar(&cfg.tenantsFile, "tenants-file", "", "path to static tenants configuration file")
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
	if cfg.tenantsFile == "" {
		return ingestConfig{}, errors.New("-tenants-file required")
	}
	if cfg.pollInterval <= 0 {
		return ingestConfig{}, errors.New("-poll-interval must be positive")
	}
	return cfg, nil
}

type ingestServer struct {
	lis        net.Listener
	grpcServer *grpc.Server
	reg        *namespace.Registry
	flushers   []*ingest.Flusher
}

func newIngestServer(ctx context.Context, cfg ingestConfig) (*ingestServer, error) {
	reg, err := namespace.LoadRegistry(ctx, cfg.tenantsFile)
	if err != nil {
		return nil, fmt.Errorf("load tenants registry: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			reg.Close()
		}
	}()

	ingesters := make(map[string]grpcapi.Ingester)
	var flushers []*ingest.Flusher

	for tenant, store := range reg.Stores() {
		ing, err := ingest.NewIngester(store)
		if err != nil {
			return nil, fmt.Errorf("create ingester for tenant %s: %w", tenant, err)
		}
		flusher, err := ingest.NewFlusher(store, ingest.Config{
			PollInterval: cfg.pollInterval,
		})
		if err != nil {
			return nil, fmt.Errorf("create flusher for tenant %s: %w", tenant, err)
		}
		ingesters[tenant] = ing
		flushers = append(flushers, flusher)
	}

	srv, err := grpcapi.NewIngestServer(ingesters)
	if err != nil {
		return nil, fmt.Errorf("create ingest server: %w", err)
	}

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMsgSize),
		grpc.MaxSendMsgSize(maxMsgSize),
		grpc.UnaryInterceptor(grpcapi.TenantInterceptor),
	)
	cloudyneighpb.RegisterIngestServiceServer(grpcServer, srv)

	lis, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", cfg.addr, err)
	}

	ok = true
	return &ingestServer{
		lis:        lis,
		grpcServer: grpcServer,
		reg:        reg,
		flushers:   flushers,
	}, nil
}

func (s *ingestServer) Addr() net.Addr {
	return s.lis.Addr()
}

func (s *ingestServer) Serve(ctx context.Context) (err error) {
	defer func() {
		if closeErr := s.reg.Close(); closeErr != nil {
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

	for _, flusher := range s.flushers {
		g.Go(func() error {
			if err := flusher.Run(flusherCtx); err != nil {
				s.grpcServer.Stop()
				return err
			}
			return nil
		})
	}

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
	slog.Info("ingest server listening", "addr", srv.Addr().String(), "tenants_file", cfg.tenantsFile)

	return srv.Serve(ctx)
}

type queryConfig struct {
	addr         string
	tenantsFile  string
	syncInterval time.Duration
	debug        bool
}

func parseQueryFlags(args []string) (queryConfig, error) {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)

	var cfg queryConfig
	fs.StringVar(&cfg.addr, "addr", ":50052", "address:port to listen on")
	fs.StringVar(&cfg.tenantsFile, "tenants-file", "", "path to static tenants configuration file")
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
	if cfg.tenantsFile == "" {
		return queryConfig{}, errors.New("-tenants-file required")
	}
	if cfg.syncInterval <= 0 {
		return queryConfig{}, errors.New("-sync-interval must be positive")
	}
	return cfg, nil
}

type queryServer struct {
	lis        net.Listener
	grpcServer *grpc.Server
	reg        *namespace.Registry
	engines    []*query.Engine
}

func newQueryServer(ctx context.Context, cfg queryConfig) (*queryServer, error) {
	reg, err := namespace.LoadRegistry(ctx, cfg.tenantsFile)
	if err != nil {
		return nil, fmt.Errorf("load tenants registry: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			reg.Close()
		}
	}()

	engines := make(map[string]grpcapi.QueryEngine)
	var runners []*query.Engine

	for tenant, store := range reg.Stores() {
		engine, err := query.NewEngine(store, cfg.syncInterval)
		if err != nil {
			return nil, fmt.Errorf("create query engine for tenant %s: %w", tenant, err)
		}
		engines[tenant] = engine
		runners = append(runners, engine)
	}

	srv, err := grpcapi.NewQueryServer(engines)
	if err != nil {
		return nil, fmt.Errorf("create query server: %w", err)
	}

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMsgSize),
		grpc.MaxSendMsgSize(maxMsgSize),
		grpc.UnaryInterceptor(grpcapi.TenantInterceptor),
	)
	cloudyneighpb.RegisterQueryServiceServer(grpcServer, srv)

	lis, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", cfg.addr, err)
	}

	ok = true
	return &queryServer{
		lis:        lis,
		grpcServer: grpcServer,
		reg:        reg,
		engines:    runners,
	}, nil
}

func (s *queryServer) Addr() net.Addr {
	return s.lis.Addr()
}

func (s *queryServer) Serve(ctx context.Context) (err error) {
	defer func() {
		if closeErr := s.reg.Close(); closeErr != nil {
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

	for _, eng := range s.engines {
		g.Go(func() error {
			if err := eng.Run(syncCtx); err != nil {
				s.grpcServer.Stop()
				return err
			}
			return nil
		})
	}

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
	slog.Info("query server listening", "addr", srv.Addr().String(), "tenants_file", cfg.tenantsFile)

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
