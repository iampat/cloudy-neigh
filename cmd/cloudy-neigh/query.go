package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
)

func newQueryCommand() *cobra.Command {
	var addr, namespace, id string

	cmd := &cobra.Command{
		Use:   "query",
		Short: "Fetch one document by ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return query(cmd.Context(), addr, namespace, id)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "localhost:8080", "server address")
	cmd.Flags().StringVar(&namespace, "namespace", "repo", "namespace to read from")
	cmd.Flags().StringVar(&id, "id", "", "document ID")
	return cmd
}

func query(ctx context.Context, addr, namespace, id string) error {
	if id == "" {
		return errors.New("--id is required")
	}

	conn, client, err := dial(addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := client.Query(ctx, queryByID(namespace, id))
	if err != nil {
		return fmt.Errorf("query %q: %w", id, err)
	}
	out, err := protojson.MarshalOptions{Multiline: true}.Marshal(resp)
	if err != nil {
		return fmt.Errorf("encode response: %w", err)
	}
	fmt.Println(string(out))
	return nil
}
