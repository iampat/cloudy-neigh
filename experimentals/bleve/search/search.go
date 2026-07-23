package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/iampat/cloudy-neigh/lsh"
	"github.com/iampat/cloudy-neigh/openai"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search/query"
	"github.com/fatih/color"
)

var (
	yellow  = color.New(color.FgYellow).SprintFunc()
	green   = color.New(color.FgGreen).SprintFunc()
	magenta = color.New(color.FgMagenta).SprintFunc()
)

var (
	indexDir         = flag.String("index", "", "where to load the data from")
	lshSize          = flag.Int("lsh_size", 10, "number of hash functions in LSH.")
	maxNumberOfItems = flag.Int("max_number_of_items", 10, "number of hash functions in LSH.")
)

// matchField builds a match query scoped to a single field.
func matchField(text, field string, fuzziness int) query.Query {
	q := bleve.NewMatchQuery(text)
	q.SetField(field)
	q.SetFuzziness(fuzziness)
	return q
}

func runQuery(q query.Query, index bleve.Index) string {
	req := bleve.NewSearchRequestOptions(q, *maxNumberOfItems, 0, false)
	req.Fields = []string{"title", "url"}

	res, err := index.Search(req)
	if err != nil {
		log.Fatalf("error executing search: %v", err)
	}
	results := make([]string, 0, len(res.Hits))
	for _, hit := range res.Hits {
		results = append(results, fmt.Sprintf("Title: %s\turl: %s", hit.Fields["title"], hit.Fields["url"]))
	}
	return strings.Join(results, "\n")
}

func RunFullTextSearchTitle(query string, index bleve.Index) {
	defer func(tStart time.Time) {
		fmt.Println("Elapsed Time:", yellow(time.Since(tStart)))
		fmt.Println(magenta("-------------------------------- Finished ---------------------------------"))
		fmt.Println()
	}(time.Now())
	fmt.Println(green("------------------------ Full Text Search (Title) Started -------------------------"))
	fuzziness := 0
	q := matchField(query, "title", fuzziness)
	fmt.Println(runQuery(q, index))
}

func RunFullTextSearchTitleAndContent(query string, index bleve.Index) {
	defer func(tStart time.Time) {
		fmt.Println("Elapsed Time:", yellow(time.Since(tStart)))
		fmt.Println(magenta("-------------------------------- Finished ---------------------------------"))
		fmt.Println()
	}(time.Now())
	fmt.Println(green("--------------- Full Text Search (Title & Content) Started ----------------"))
	fuzziness := 0
	q := bleve.NewDisjunctionQuery(
		matchField(query, "title", fuzziness),
		matchField(query, "text", fuzziness),
	)
	fmt.Println(runQuery(q, index))
}

func RunVectorSearch(query string, index bleve.Index) {
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
	q := bleve.NewBooleanQuery()
	q.AddShould(bleve.NewDisjunctionQuery(
		matchField(hash, "text_lsh_hash", 1),
		matchField(hash, "title_lsh_hash", 1),
	))
	q.AddShould(bleve.NewDisjunctionQuery(
		matchField(query, "title", fuzziness),
		matchField(query, "text", fuzziness),
	))
	fmt.Println(runQuery(q, index))
}

func main() {
	flag.Parse()
	index, err := bleve.Open(*indexDir)
	if err != nil {
		log.Fatalf("unable to open reader: %v", err)
	}
	defer func() {
		if err := index.Close(); err != nil {
			log.Fatalf("error closing reader: %v", err)
		}
	}()
	fmt.Println("Warmup")
	runQuery(matchField("WARMUP", "title", 0), index)
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("Press Ctrl-D to exit.")
	fmt.Printf("query: ")
	for scanner.Scan() {
		query := scanner.Text()
		RunFullTextSearchTitle(query, index)
		RunFullTextSearchTitleAndContent(query, index)
		RunVectorSearch(query, index)
		fmt.Printf("query: ")
	}
	if err := scanner.Err(); err != nil {
		log.Fatalln("oops!", err)
	}
}
