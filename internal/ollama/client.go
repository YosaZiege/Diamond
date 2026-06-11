// OpenAI-compatible LLM client.
// Works with DeepSeek (https://api.deepseek.com), Ollama (/v1 endpoint), or any OpenAI-compat API.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// New creates a client.
// baseURL: "https://api.deepseek.com" for DeepSeek, "http://localhost:11434/v1" for Ollama.
// apiKey:  Bearer token; empty for local Ollama.
func New(baseURL, apiKey, model string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *Client) Chat(ctx context.Context, system, user string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"stream": false,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("LLM error %d: %s", resp.StatusCode, string(b))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("empty response from LLM")
	}
	return result.Choices[0].Message.Content, nil
}

// Ping checks reachability. For external APIs it verifies the key via GET /models.
// For local Ollama (no apiKey) it hits /models on the base URL.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/models", nil)
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return fmt.Errorf("unreachable: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// KeepWarm periodically pings the LLM to keep a local model loaded in RAM.
// No-op for external API providers (apiKey set) — cloud APIs don't evict models.
func (c *Client) KeepWarm(ctx context.Context, interval time.Duration) {
	if c.apiKey != "" {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		c.warmUp(ctx)
		for {
			select {
			case <-ticker.C:
				c.warmUp(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (c *Client) warmUp(ctx context.Context) {
	wCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Minimal chat to keep the model in RAM
	c.Chat(wCtx, "", "ping") //nolint:errcheck
}
