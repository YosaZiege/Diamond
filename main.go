package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/yosa/diamond/internal/server"
)

func main() {
	cfg := server.Config{
		OllamaURL: getenv("DIAMOND_OLLAMA_URL", "http://localhost:11434"),
		Model:     getenv("DIAMOND_MODEL", "qwen2.5-coder:7b"),
		Port:      getenv("DIAMOND_PORT", "7331"),
		DataDir:   getenv("DIAMOND_DATA_DIR", "/var/lib/diamond"),
	}

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("failed to init server: %v", err)
	}

	// Keep model hot in Ollama's RAM — ping every 25 seconds
	go srv.KeepWarm(context.Background(), 25*time.Second)

	log.Printf("Diamond server on :%s | Ollama: %s | Model: %s", cfg.Port, cfg.OllamaURL, cfg.Model)
	if err := http.ListenAndServe(":"+cfg.Port, srv); err != nil {
		log.Fatal(err)
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

