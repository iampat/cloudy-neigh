package main

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"runtime"
	"time"

	"github.com/blevesearch/bleve/v2"
)

func search(hashQuery string, index bleve.Index, fuzziness int) map[int]int {
	q := bleve.NewMatchQuery(hashQuery)
	q.SetField("hash")
	q.SetFuzziness(fuzziness)
	req := bleve.NewSearchRequestOptions(q, 1000000, 0, false)
	req.Fields = []string{"hash"}

	res, err := index.Search(req)
	if err != nil {
		log.Fatalf("error executing search: %v", err)
	}

	freq := map[int]int{}
	for _, hit := range res.Hits {
		value, ok := hit.Fields["hash"].(string)
		if !ok {
			continue
		}
		dist := 0
		for idx, b := range []byte(hashQuery) {
			if idx < len(value) && b != value[idx] {
				dist++
			}
		}
		freq[dist]++
	}
	return freq
}

func hashIndex(num int) string {
	return fmt.Sprintf("%020b", num)
}

func main() {
	runtime.GOMAXPROCS(1)
	indexDir, err := os.MkdirTemp("", "")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("indexDir:", indexDir)
	index, err := bleve.New(indexDir, bleve.NewIndexMapping())
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if cerr := index.Close(); cerr != nil {
			log.Fatal(cerr)
		}
	}()
	var (
		num  int = 0
		bnum string
	)
	batch := index.NewBatch()
	for rep := 0; rep < 100; rep++ { // 100 item per bucket
		for num = 0; num < 0b100000000000000000000; num++ { // 2^20 = 1 million buckets
			bnum = hashIndex(num)

			id := fmt.Sprintf("ID-%d-%d-%d-%d", rand.Int(), num, rep, rand.Int())
			if err := batch.Index(id, map[string]interface{}{"hash": bnum}); err != nil {
				log.Fatalf("error indexing doc: %v", err)
			}
		}

		log.Println("->", rep)
		if err := index.Batch(batch); err != nil {
			log.Fatalf("error executing batch: %v", err)
		}
		batch.Reset()
	}

	log.Printf("Index is ready with %d (%s) fake items.\n", num, bnum)

	for _, numQuery := range []int{1, 1, 10, 12, 25, 3434, 5456, 5000, 232443, 532322, 743343} {
		hashQuery := hashIndex(numQuery)
		fmt.Printf("Query: %d (%s)\n", numQuery, hashQuery)
		ts := time.Now()
		freq := search(hashQuery, index, 1)

		fmt.Println("Freq:", freq, "\t", time.Since(ts))
	}
}
