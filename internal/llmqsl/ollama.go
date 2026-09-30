package llmqsl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OllamaClient calls a local Ollama server's /api/generate endpoint.
type OllamaClient struct {
	BaseURL    string
	Model      string
	Timeout    time.Duration
	System     string
	Temperature float64
	JSONMode   bool // request format: "json" if true
	HTTP       *http.Client
}

func NewOllamaClient(baseURL, model string) *OllamaClient {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "ministral-3b"
	}
	return &OllamaClient{
		BaseURL:     baseURL,
		Model:       model,
		Timeout:     2 * time.Minute,
		Temperature: 0,
		HTTP:        &http.Client{Timeout: 2 * time.Minute},
	}
}

func (c *OllamaClient) Generate(ctx context.Context, prompt string) (string, error) {
	type reqBody struct {
		Model       string  `json:"model"`
		Prompt      string  `json:"prompt"`
		System      string  `json:"system,omitempty"`
		Stream      bool    `json:"stream"`
		Temperature float64 `json:"temperature,omitempty"`
		Format      string  `json:"format,omitempty"`
	}
	body := reqBody{
		Model:       c.Model,
		Prompt:      prompt,
		System:      c.System,
		Stream:      false,
		Temperature: c.Temperature,
	}
	if c.JSONMode {
		body.Format = "json"
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal ollama request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/api/generate", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("build ollama request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama generate: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read ollama response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed struct {
		Response string `json:"response"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("parse ollama response: %w (body=%s)", err, string(respBody))
	}
	if parsed.Error != "" {
		return "", fmt.Errorf("ollama: %s", parsed.Error)
	}
	return parsed.Response, nil
}
