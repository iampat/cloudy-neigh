package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"runtime"
	"time"

	"github.com/blugelabs/bluge"
)

func search(hashQuery string, indexReader *bluge.Reader, fuzziness int) map[int]int {
	q := bluge.NewMatchQuery(hashQuery).SetField("hash").SetFuzziness(fuzziness)
	req := bluge.NewAllMatches(q)
	dmi, err := indexReader.Search(context.Background(), req)
	if err != nil {
		log.Fatalf("error executing search: %v", err)
	}

	next, err := dmi.Next()
	freq := map[int]int{}
	for err == nil && next != nil {
		err = next.VisitStoredFields(func(field string, value []byte) bool {
			if field == "hash" {
				diff := ""
				dist := 0
				for idx, b := range []byte(hashQuery) {
					if b == value[idx] {
						diff = diff + "."
					} else {
						diff = diff + "x"
						dist++
					}
				}
				// fmt.Printf("%s\t%s \t %d (%s)\n", field, string(value), dist, diff)
				freq[dist]++
			} else {
				//	fmt.Println(field, "\t", string(value))
			}
			return true
		})
		if err != nil {
			log.Fatalf("error accessing stored fields: %v", err)
		}
		// fmt.Println("Score:", next.Score)
		// fmt.Println("----------------------------------")
		next, err = dmi.Next()
	}
	if err != nil {
		log.Fatalf("error iterating results: %v", err)
	}
	return freq
}

func hashIndex(num int) string {
	return fmt.Sprintf("%020b", num)
}

func main() {
	runtime.GOMAXPROCS(1)
	indexDir, err := os.MkdirTemp("", "")

	fmt.Println("indexDir:", indexDir)
	cfg := bluge.DefaultConfig(indexDir)
	indexWriter, err := bluge.OpenWriter(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		cerr := indexWriter.Close()
		if cerr != nil {
			log.Fatal(cerr)
		}
	}()
	var num int = 0
	var bnum string
	batch := bluge.NewBatch()
	for rep := 0; rep < 100; rep++ { // 100 item per bucket
		for num = 0; num < 0b100000000000000000000; num++ { // 2^20 = 1 million buckets
			bnum = hashIndex(num)

			id := fmt.Sprintf("ID-%d-%d-%d-%d", rand.Int(), num, rep, rand.Int())
			bdoc := bluge.NewDocument(id).
				AddField(bluge.NewTextField("hash", bnum).StoreValue())
			batch.Update(bdoc.ID(), bdoc)
		}

		log.Println("->", rep)
		err = indexWriter.Batch(batch)
		if err != nil {
			log.Fatalf("error executing batch: %v", err)
		}
		batch.Reset()
	}

	log.Printf("Index is ready with %d (%s) fake items.\n", num, bnum)

	indexReader, err := indexWriter.Reader()
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		err = indexReader.Close()
		if err != nil {
			log.Fatalf("error closing reader: %v", err)
		}
	}()
	for _, numQuery := range []int{1, 1, 10, 12, 25, 3434, 5456, 5000, 232443, 532322, 743343} {

		hashQuery := hashIndex(numQuery)
		fmt.Printf("Query: %d (%s)\n", numQuery, hashQuery)
		ts := time.Now()
		freq := search(hashQuery, indexReader, 1)

		fmt.Println("Freq:", freq, "\t", time.Since(ts))
	}
}
