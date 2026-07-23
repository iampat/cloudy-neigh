package openai

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"

	"github.com/iampat/cloudy-neigh/vector"
)

type client struct {
	key      string
	endpoint string
	model    string
	dim      int
}

func NewClient(key string) *client {
	return &client{
		key:      key,
		endpoint: "https://api.openai.com/v1/embeddings",
		model:    "text-embedding-ada-002",
		dim:      1536,
	}
}

func (c *client) Embeddings(input []string) ([]*vector.Vector32, error) {
	embd, _, err := c.EmbeddingsWithCost(input)
	return embd, err
}

func (c *client) EmbeddingsWithCost(input []string) ([]*vector.Vector32, int, error) {
	// Set the request body parameters
	reqBody := struct {
		Input []string `json:"input"`
		Model string   `json:"model"`
	}{
		Input: input,
		Model: c.model,
	}
	j, err := json.Marshal(reqBody)
	if err != nil {
		log.Fatalln(err)
	}
	req, err := http.NewRequest(http.MethodPost, c.endpoint, bytes.NewBuffer(j))
	if err != nil {
		log.Fatalln(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}

	defer res.Body.Close()

	buf := new(bytes.Buffer)
	buf.ReadFrom(res.Body)

	if res.StatusCode > 299 {
		log.Fatalln("oops!", buf.String())
	}

	resBody := struct {
		Object string `json:"object"`
		Data   []struct {
			Object    string    `json:"object"`
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
		Model string `json:"model"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		}
	}{}
	json.NewDecoder(buf).Decode(&resBody)
	if len(resBody.Data) != len(input) {
		log.Fatalf("oops! len(input)=%d  len(response)=%d\n", len(input), len(resBody.Data))
	}
	embeddings := make([]*vector.Vector32, len(resBody.Data))
	for idx, d := range resBody.Data {
		if idx != d.Index {
			log.Fatalf("oops! %d != %d", idx, d.Index)
		}
		embeddings[idx] = &vector.Vector32{
			Values: d.Embedding,
		}

	}
	return embeddings, resBody.Usage.TotalTokens, nil
}

func (c *client) EmbeddingDim() int {
	return c.dim
}
