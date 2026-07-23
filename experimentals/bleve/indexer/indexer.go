package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/iampat/cloudy-neigh/document"

	"github.com/blevesearch/bleve/v2"
)

const (
	maxLineSize      int = 1000 * 1000 // Reserve 1MB
	verbosity        int = 10000
	maxNumberOfItems     = 10 * 1000 * 1000
)

var (
	inputJson   = flag.String("input_json", "", "where to load the data")
	outputIndex = flag.String("output_index", "", "where to write the index")
)

func main() {
	defer func(tStart time.Time) {
		fmt.Println("Elapsed Time:", time.Since(tStart))
	}(time.Now())
	flag.Parse()

	log.Println("input json file:", *inputJson)
	readFile, err := os.Open(*inputJson)
	if err != nil {
		log.Fatalln(err)
	}
	defer readFile.Close()
	fileScanner := bufio.NewScanner(readFile)

	index, err := bleve.New(*outputIndex, bleve.NewIndexMapping())
	if err != nil {
		log.Fatalln(err)
	}
	defer func() {
		if cerr := index.Close(); cerr != nil {
			log.Fatalln(cerr)
		}
	}()

	counter := 0
	batch := index.NewBatch()
	fileScanner.Buffer(make([]byte, maxLineSize), maxLineSize)
	for fileScanner.Scan() {
		data := fileScanner.Bytes()
		inputDoc := &document.Document{}

		if err := json.Unmarshal(data, inputDoc); err != nil {
			log.Fatalln(err)
		}
		textEmbedding, err := json.Marshal(inputDoc.TextEmbedding)
		if err != nil {
			log.Fatalln(err)
		}
		titleEmbedding, err := json.Marshal(inputDoc.TitleEmbedding)
		if err != nil {
			log.Fatalln(err)
		}
		bdoc := map[string]interface{}{
			"text_embedding":  string(textEmbedding),
			"title_embedding": string(titleEmbedding),
			"text_lsh_hash":   inputDoc.TextLshHash,
			"title_lsh_hash":  inputDoc.TextLshHash,
			"url":             inputDoc.Url,
			"text":            inputDoc.Text,
			"title":           inputDoc.Title,
		}
		if err := batch.Index(inputDoc.Id, bdoc); err != nil {
			log.Fatalln(err)
		}

		counter++
		if counter < 5 {
			log.Println("sample doc:", inputDoc.Title)
		}
		if counter%verbosity == 0 {
			if err := index.Batch(batch); err != nil {
				log.Fatalf("error executing batch: %v", err)
			}
			batch.Reset()
			log.Printf("%d items have been added", counter)
		}
		if counter == maxNumberOfItems {
			break
		}
	}
	if fileScanner.Err() != nil {
		log.Fatalln(fileScanner.Err())
	}
	if err := index.Batch(batch); err != nil {
		log.Fatalf("error executing batch: %v", err)
	}
	batch.Reset()
	log.Printf("%d items have been added", counter)

	log.Printf("document indexed: %s", *outputIndex)
}
