package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newRootCommand() *cobra.Command {
	var logLevel string

	root := &cobra.Command{
		Use:           "cloudy-neigh",
		Short:         "A cloud-native search engine",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// CONSIDER(ali): a flag reads from the command line and nowhere
			// else. An environment variable and a config file bind here, before
			// any command reads a flag. What wins, and in what order?
			return setupLogging(logLevel)
		},
	}
	root.PersistentFlags().StringVar(&logLevel, "log-level", "info", "debug, info, warn or error")

	root.AddCommand(newServeCommand(), newIngestCommand(), newQueryCommand())
	return root
}

func resolvePath(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	invoked := os.Getenv("BUILD_WORKING_DIRECTORY")
	if invoked == "" {
		return path
	}
	return filepath.Join(invoked, path)
}

func setupLogging(level string) error {
	var parsed slog.Level
	if err := parsed.UnmarshalText([]byte(level)); err != nil {
		return fmt.Errorf("parse log level %q: %w", level, err)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parsed})))
	return nil
}
