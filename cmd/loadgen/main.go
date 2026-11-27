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
	Model     string        `json:"model"`
	Stream    bool          `json:"stream"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens *int          `json:"max_tokens,omitempty"`
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
	maxTokens := flag.Int("max-tokens", 0, "max_tokens in the request body; 0 omits the field")
	apiKey := flag.String("api-key", os.Getenv("FIREPOINT_API_KEY"), "virtual key sent as Authorization; empty sends no key")
	split := flag.Duration("split", 0, "also record this prefix of the measured window separately; 0 keeps one window")
	flag.Parse()

	if *rate <= 0 || *duration <= 0 || *concurrency < 1 || *warmup < 0 {
		log.Fatal("rate and duration must be positive, concurrency at least 1, warmup at least 0")
	}
	if strings.TrimSpace(*model) == "" {
		log.Fatal("model must not be empty")
	}
	body, err := chatBodyJSON(*model, *maxTokens)
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
		Split:       *split,
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

// chatBodyJSON is the streaming chat loadgen sends. maxTokens of 0 leaves
// the field out so a mock benchmark stays the same shape it has always been.
// A live run sets it so the model cannot generate without a cap.
func chatBodyJSON(model string, maxTokens int) ([]byte, error) {
	body := chatBody{Model: model, Stream: true, Messages: []chatMessage{{Role: "user", Content: "hi"}}}
	if maxTokens > 0 {
		body.MaxTokens = &maxTokens
	}
	return json.Marshal(body)
}

func writeJSON(path string, report Report) error {
	buf, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	return os.WriteFile(path, buf, 0o644)
}
