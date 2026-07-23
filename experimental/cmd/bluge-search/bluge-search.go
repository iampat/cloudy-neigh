package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/iampat/cloudy-neigh/lsh"
	"github.com/iampat/cloudy-neigh/openai"

	"github.com/blugelabs/bluge"
	"github.com/fatih/color"
)

var (
	yellow  = color.New(color.FgYellow).SprintFunc()
	red     = color.New(color.FgRed).SprintFunc()
	green   = color.New(color.FgGreen).SprintFunc()
	magenta = color.New(color.FgMagenta).SprintFunc()
)

var (
	indexDir         = flag.String("index", "", "where to load the data from")
	lshSize          = flag.Int("lsh_size", 10, "number of hash functions in LSH.")
	maxNumberOfItems = flag.Int("max_number_of_items", 10, "number of hash functions in LSH.")
)

func runQuery(q bluge.Query, indexReader *bluge.Reader) string {
	req := bluge.NewTopNSearch(*maxNumberOfItems, q)
	// req.SortByCustom()

	dmi, err := indexReader.Search(context.Background(), req)
	if err != nil {
		log.Fatalf("error executing search: %v", err)
	}
	next, err := dmi.Next()
	results := []string{}
	for err == nil && next != nil {
		values := map[string]string{}
		err = next.VisitStoredFields(func(field string, value []byte) bool {
			values[field] = string(value)
			return true
		})
		if err != nil {
			log.Fatalf("error accessing stored fields: %v", err)
		}
		results = append(results, fmt.Sprintf("Title: %s\turl: %s", values["title"], values["url"]))
		next, err = dmi.Next()
	}
	if err != nil {
		log.Fatalf("error iterating results: %v", err)
	}
	return strings.Join(results, "\n")
}

func RunFullTextSearchTitle(query string, indexReader *bluge.Reader) {
	defer func(tStart time.Time) {
		fmt.Println("Elapsed Time:", yellow(time.Since(tStart)))
		fmt.Println(magenta("-------------------------------- Finished ---------------------------------"))
		fmt.Println()
	}(time.Now())
	fmt.Println(green("------------------------ Full Text Search (Title) Started -------------------------"))
	fuzziness := 0
	// if len(query) > 10 {
	// 	fuzziness = 1
	// }
	q := bluge.NewMatchQuery(query).SetField("title").SetFuzziness(fuzziness)
	fmt.Println(runQuery(q, indexReader))
}

func RunFullTextSearchTitleAndContent(query string, indexReader *bluge.Reader) {
	defer func(tStart time.Time) {
		fmt.Println("Elapsed Time:", yellow(time.Since(tStart)))
		fmt.Println(magenta("-------------------------------- Finished ---------------------------------"))
		fmt.Println()
	}(time.Now())
	fmt.Println(green("--------------- Full Text Search (Title & Content) Started ----------------"))
	fuzziness := 0
	// if len(query) > 10 {
	// 	fuzziness = 1
	// }
	q := bluge.NewMatchQuery(query).
		SetField("title").SetFuzziness(fuzziness).
		SetField("text").SetFuzziness(fuzziness)

	fmt.Println(runQuery(q, indexReader))
}

func RunVectorSearch(query string, indexReader *bluge.Reader) {
	tStart := time.Now()
	var tEmbeddingDone time.Time
	defer func(tStart, tEmbeddingDone *time.Time) {
		fmt.Printf("Elapsed Time: %s (OpenAI latency: %s)\n", yellow(time.Since(*tStart)), yellow(tEmbeddingDone.Sub(*tStart)))
		fmt.Println(magenta("-------------------------------- Finished ---------------------------------"))
		fmt.Println()
	}(&tStart, &tEmbeddingDone)
	fmt.Println(green("------------------------- Vector Search Started ---------------------------"))
	client := openai.NewClient(os.Getenv("OPENAI_API_KEY"))
	lsh := lsh.NewLSH42(*lshSize, client.EmbeddingDim())
	embd, err := client.Embeddings([]string{query})
	tEmbeddingDone = time.Now()
	if err != nil {
		log.Fatalln("ERROR in calling Open AI API", err)
	}

	hash := lsh.Hash(embd[0])
	fuzziness := 0
	// if len(query) > 10 {
	// 	fuzziness = 1
	// }
	q := bluge.NewBooleanQuery().
		AddShould(
			bluge.NewMatchQuery(hash).
				SetField("text_lsh_hash").SetFuzziness(1).
				SetField("title_lsh_hash").SetFuzziness(1),
		).
		AddShould(bluge.NewMatchQuery(query).
			SetField("title").SetFuzziness(fuzziness).
			SetField("text").SetFuzziness(fuzziness))
	fmt.Println(runQuery(q, indexReader))
}

func main() {
	flag.Parse()
	cfg := bluge.DefaultConfig(*indexDir)
	indexReader, err := bluge.OpenReader(cfg)
	if err != nil {
		log.Fatalf("unable to open reader: %v", err)
	}
	defer func() {
		err = indexReader.Close()
		if err != nil {
			log.Fatalf("error closing reader: %v", err)
		}
	}()
	fmt.Println("Warmup")
	runQuery(bluge.NewMatchQuery("WARMUP").SetField("title"), indexReader)
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("Press Ctrl-D to exit.")
	fmt.Printf("query: ")
	for scanner.Scan() {
		query := scanner.Text()
		RunFullTextSearchTitle(query, indexReader)
		RunFullTextSearchTitleAndContent(query, indexReader)
		RunVectorSearch(query, indexReader)
		fmt.Printf("query: ")
	}
	if err := scanner.Err(); err != nil {
		log.Fatalln("oops!", err)
	}
}
