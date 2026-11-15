package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatBody struct {
	Model    string        `json:"model"`
	Stream   bool          `json:"stream"`
	Messages []chatMessage `json:"messages"`
}

// version is replaced by the image build: -ldflags "-X main.version=...".
var version = "dev"

func main() {
	log.Printf("loadgen %s", version)
	urlFlag := flag.String("url", "http://127.0.0.1:8080/v1/chat/completions", "streaming chat completions URL")
	rate := flag.Float64("rate", 1, "requests per second; ticks fire even when responses are still open")
	duration := flag.Duration("duration", 30*time.Second, "measurement window after warmup")
	warmup := flag.Duration("warmup", 10*time.Second, "initial period whose results are discarded")
	concurrency := flag.Int("concurrency", 8, "maximum requests in flight")
	jsonPath := flag.String("json", "loadgen.json", "file for the JSON summary")
	model := flag.String("model", "mock", "model name in the request body")
	apiKey := flag.String("api-key", os.Getenv("FIREROUTER_API_KEY"), "virtual key sent as Authorization; empty sends no key")
	flag.Parse()

	if *rate <= 0 || *duration <= 0 || *concurrency < 1 || *warmup < 0 {
		log.Fatal("rate and duration must be positive, concurrency at least 1, warmup at least 0")
	}
	if strings.TrimSpace(*model) == "" {
		log.Fatal("model must not be empty")
	}
	body, err := json.Marshal(chatBody{Model: *model, Stream: true, Messages: []chatMessage{{Role: "user", Content: "hi"}}})
	if err != nil {
		log.Fatal(err)
	}

	report, err := Run(context.Background(), Config{
		URL:         *urlFlag,
		Rate:        *rate,
		Duration:    *duration,
		Warmup:      *warmup,
		Concurrency: *concurrency,
		Now:         time.Now,
		APIKey:      *apiKey,
		Body:        body,
	})
	if err != nil {
		log.Fatal(err)
	}
	printReport(os.Stdout, report)
	if err := writeJSON(*jsonPath, report); err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stdout, "wrote %s\n", *jsonPath)
}

func writeJSON(path string, report Report) error {
	buf, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	return os.WriteFile(path, buf, 0o644)
}
