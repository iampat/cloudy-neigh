Tests pass (`//cmd/cloudy:cloudy_test`, `//grpcapi:grpcapi_test`, both under `--config=race`). Findings, ranked by severity:

**1. The stub answers a valid query with an empty success. `grpcapi/query.go:27`**
A client cannot tell "no results" from "not implemented". That is a silent default (`docs/guidelines/go.md`, Simplicity). Return the truth until the engine lands:

```go
return nil, status.Error(codes.Unimplemented, "grpcapi: query not implemented")
```

Then change `TestQuery_Success` in `grpcapi/query_test.go:47` to assert `codes.Unimplemented` for a valid namespace.

**2. The query path opens a store and parses a poll interval that nothing reads. `cmd/cloudy/main.go:215,255`**
`queryConfig.pollInterval` is parsed and validated, then never read. `newQueryServer` opens `objectstore`, holds it, and closes it. No code reads from it, and `grpcapi.NewQueryServer()` takes no dependencies. The `-url` flag exists only to feed that unused store. This breaks "Do not solve a problem we do not have" (`.claude/CLAUDE.md`). Delete `url` and `pollInterval` from `queryConfig` with their flags and validation. Delete `store` from `queryServer`, the `objectstore.Open` call at `main.go:255`, and the close defer at `main.go:286`. Add each back when the query engine consumes it.

**3. Internal tests in the binary package. `cmd/cloudy/main_test.go:1`**
`docs/guidelines/go.md` (Tests): binary packages need no tests unless required, tests use an external package, and every internal test needs a justification. The file supplies none. It also tests the standard library: `TestParseQueryFlags_Help` (`main_test.go:96`) asserts that `flag` returns `flag.ErrHelp`. That breaks "Do not assert what you can assume already works". The ingest flags set the precedent and have no test. Delete `main_test.go` and the `cloudy_test` target in `cmd/cloudy/BUILD.bazel:25`.

**4. The graceful-stop block now exists twice. `cmd/cloudy/main.go:292-319`**
It duplicates `main.go:149-181` except for the flusher calls. Extract one helper:

```go
func serveGRPC(ctx context.Context, srv *grpc.Server, lis net.Listener) error {
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(lis) }()

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
```

The ingest goroutine at `main.go:148` becomes:

```go
g.Go(func() error {
	err := serveGRPC(ctx, s.grpcServer, s.lis)
	cancelFlusher()
	return err
})
```

`queryServer.Serve` becomes the store-close defer plus `return serveGRPC(ctx, s.grpcServer, s.lis)`. This also removes a small drift: the query copy skips the `ErrServerStopped` check in the plain-exit branch at `main.go:317`, and the ingest copy has it.

**5. `NewQueryServer` adds nothing. `grpcapi/query.go:16-18`**
The struct has no fields, so the zero value works (`docs/guidelines/go.md`, Less code and Zero values). Delete the constructor. Callers write `new(grpcapi.QueryServer)`. Reintroduce a constructor when the server gets dependencies. If finding 1 stays out, skip this one for API stability across the next PR.
