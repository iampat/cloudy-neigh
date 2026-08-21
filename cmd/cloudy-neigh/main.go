// Command cloudy-neigh serves the Index service and drives it from a terminal.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := newRootCommand().ExecuteContext(ctx); err != nil {
		slog.Error("cloudy-neigh failed", "error", err)
		os.Exit(1)
	}
}
