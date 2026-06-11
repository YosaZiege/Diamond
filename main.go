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
		LLMURL:  getenv("DIAMOND_LLM_URL", "https://api.deepseek.com"),
		APIKey:  getenv("DIAMOND_LLM_KEY", ""),
		Model:   getenv("DIAMOND_MODEL", "deepseek-chat"),
		Port:    getenv("DIAMOND_PORT", "7331"),
		DataDir: getenv("DIAMOND_DATA_DIR", "/var/lib/diamond"),
	}

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("failed to init server: %v", err)
	}

	// Keep model warm in RAM — no-op for external API providers
	go srv.KeepWarm(context.Background(), 25*time.Second)

	log.Printf("Diamond server on :%s | LLM: %s | Model: %s", cfg.Port, cfg.LLMURL, cfg.Model)
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
