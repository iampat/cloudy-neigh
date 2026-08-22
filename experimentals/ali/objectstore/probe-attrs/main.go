package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"gocloud.dev/blob"
	"gocloud.dev/blob/fileblob"
)

func main() {
	ctx := context.Background()
	dir := "probe-attrs/tmp"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	b, err := fileblob.OpenBucket(dir, nil)
	if err != nil {
		panic(err)
	}
	defer b.Close()

	err = b.WriteAll(ctx, "docs/a.txt", []byte("hello world"), &blob.WriterOptions{
		Metadata:    map[string]string{"generation": "42"},
		ContentType: "text/plain",
	})
	if err != nil {
		panic(err)
	}
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, _ := d.Info()
		fmt.Printf("== %s (%d bytes)\n", p, info.Size())
		raw, _ := os.ReadFile(p)
		fmt.Printf("%s\n", raw)
		return nil
	})

	attrs, err := b.Attributes(ctx, "docs/a.txt")
	if err != nil {
		panic(err)
	}
	fmt.Printf("Attributes: ETag=%s MD5=%x Metadata=%v ContentType=%s\n",
		attrs.ETag, attrs.MD5, attrs.Metadata, attrs.ContentType)
}
