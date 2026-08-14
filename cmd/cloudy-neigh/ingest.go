package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"

	"github.com/iampat/cloudy-neigh/proto/cloudyneigh"
)

func newIngestCommand() *cobra.Command {
	var (
		addr, namespace    string
		maxSize, batchSize int64
	)

	cmd := &cobra.Command{
		Use:   "ingest PATH",
		Short: "Walk a directory and write every text file as a document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return ingest(cmd.Context(), resolvePath(args[0]), addr, namespace, maxSize, batchSize)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "localhost:8080", "server address")
	cmd.Flags().StringVar(&namespace, "namespace", "repo", "namespace to write into")
	cmd.Flags().Int64Var(&maxSize, "max-size", 1<<20, "skip a file larger than this many bytes")
	cmd.Flags().Int64Var(&batchSize, "batch-size", 1<<20,
		"start a new write once a batch reaches this many bytes")
	return cmd
}

func ingest(ctx context.Context, root, addr, namespace string, maxSize, batchSize int64) error {
	docs, skipped, err := collect(root, maxSize)
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		return fmt.Errorf("no text file under %s", root)
	}

	conn, client, err := dial(addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	// One Write per batch, because gRPC caps a received message at 4 MiB by
	// default. Each batch applies whole or not at all. The directory as a whole
	// does not, so a failure part way through leaves earlier batches written.
	for _, batch := range split(docs, batchSize) {
		req := &cloudyneigh.WriteRequest{
			Namespace: namespace,
			Operation: &cloudyneigh.WriteRequest_Upsert{
				Upsert: &cloudyneigh.Upsert{Documents: batch},
			},
		}
		if _, err := client.Write(ctx, req); err != nil {
			return fmt.Errorf("write: %w", err)
		}
	}
	fmt.Printf("ingested %d documents, skipped %d\n", len(docs), skipped)

	for _, doc := range docs[:min(3, len(docs))] {
		resp, err := client.Query(ctx, queryByID(namespace, doc.GetId()))
		if err != nil {
			return fmt.Errorf("query %q: %w", doc.GetId(), err)
		}
		if len(resp.GetMatches()) == 0 {
			return fmt.Errorf("query %q returned no match after a successful write", doc.GetId())
		}
		fmt.Printf("verified %s\n", doc.GetId())
	}
	return nil
}

func collect(root string, maxSize int64) ([]*cloudyneigh.Document, int, error) {
	var docs []*cloudyneigh.Document
	skipped := 0

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// One unreadable entry must not lose the whole walk. A failure on
			// the root is different: there is nothing to walk.
			if path == root {
				return err
			}
			slog.Warn("skip", "path", path, "error", err)
			skipped++
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if path != root && skipDir(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			skipped++
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			slog.Warn("skip", "path", path, "error", err)
			skipped++
			return nil
		}
		if info.Size() > maxSize {
			skipped++
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("skip", "path", path, "error", err)
			skipped++
			return nil
		}
		// A Value holds a proto string, which has to be valid UTF-8.
		if !utf8.Valid(content) {
			skipped++
			return nil
		}

		id, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("relative path of %s: %w", path, err)
		}
		docs = append(docs, &cloudyneigh.Document{
			Id: filepath.ToSlash(id),
			Attributes: map[string]*cloudyneigh.Value{
				"content": textValue(string(content)),
				"ext":     textValue(strings.TrimPrefix(filepath.Ext(path), ".")),
			},
		})
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return docs, skipped, nil
}

func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "bazel-")
}

// A document larger than size gets a batch of its own.
func split(docs []*cloudyneigh.Document, size int64) [][]*cloudyneigh.Document {
	var batches [][]*cloudyneigh.Document
	var batch []*cloudyneigh.Document
	var total int64

	for _, doc := range docs {
		n := int64(proto.Size(doc))
		if len(batch) > 0 && total+n > size {
			batches = append(batches, batch)
			batch, total = nil, 0
		}
		batch = append(batch, doc)
		total += n
	}
	if len(batch) > 0 {
		batches = append(batches, batch)
	}
	return batches
}
