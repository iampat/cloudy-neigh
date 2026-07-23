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

	"github.com/blugelabs/bluge"
)

const (
	maxLineSize      int = 1000 * 1000 // Reserve 1MB
	vebosity         int = 10000
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

	cfg := bluge.DefaultConfig(*outputIndex)
	indexWriter, err := bluge.OpenWriter(cfg)
	if err != nil {
		log.Fatalln(err)
	}
	defer func() {
		cerr := indexWriter.Close()
		if cerr != nil {
			log.Fatalln(cerr)
		}
	}()

	counter := 0
	batch := bluge.NewBatch()
	fileScanner.Buffer(make([]byte, maxLineSize), maxLineSize)
	for fileScanner.Scan() {

		data := fileScanner.Bytes()
		inputDoc := &document.Document{}

		json.Unmarshal(data, inputDoc)
		text_embedding, err := json.Marshal(inputDoc.TextEmbedding)
		if err != nil {
			log.Fatalln(err)
		}
		title_embedding, err := json.Marshal(inputDoc.TitleEmbedding)
		if err != nil {
			log.Fatalln(err)
		}
		bdoc := bluge.NewDocument(inputDoc.Id).
			AddField(bluge.NewStoredOnlyField("text_embedding", text_embedding)).
			AddField(bluge.NewStoredOnlyField("text_embedding", title_embedding)).
			AddField(bluge.NewTextField("text_lsh_hash", inputDoc.TextLshHash).StoreValue()).
			AddField(bluge.NewTextField("title_lsh_hash", inputDoc.TextLshHash).StoreValue()).
			AddField(bluge.NewTextField("url", inputDoc.Url).StoreValue()).
			AddField(bluge.NewTextField("text", inputDoc.Text).StoreValue()).
			AddField(bluge.NewTextField("title", inputDoc.Title).StoreValue())
		batch.Update(bdoc.ID(), bdoc)

		counter++
		if counter < 5 {
			log.Println("sample doc:", inputDoc.Title)
		}
		if counter%vebosity == 0 {
			err = indexWriter.Batch(batch)
			if err != nil {
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
	err = indexWriter.Batch(batch)
	if err != nil {
		log.Fatalf("error executing batch: %v", err)
	}
	batch.Reset()
	log.Printf("%d items have been added", counter)

	log.Printf("document indexed: %s", *outputIndex)
}
