package main

import (
	"fmt"
	"log"
	"os"

	"github.com/cockroachdb/pebble/v2"
)

func main() {
	dir, err := os.MkdirTemp("", "pebble-demo-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		log.Fatal(err)
	}

	key := []byte("hello")
	if err := db.Set(key, []byte("world"), pebble.Sync); err != nil {
		log.Fatal(err)
	}
	value, closer, err := db.Get(key)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s %s\n", key, value)
	if err := closer.Close(); err != nil {
		log.Fatal(err)
	}
	if err := db.Close(); err != nil {
		log.Fatal(err)
	}
}
