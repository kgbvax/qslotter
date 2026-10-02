package qpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// chatClient is a minimal OpenAI-compatible chat-completions client. Only the
// fields qpc needs are modelled; Variant.Extra reaches the request body as is.
type chatClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Temperature    *float64      `json:"temperature,omitempty"`
	Seed           *int          `json:"seed,omitempty"`
	MaxTokens      int           `json:"max_tokens,omitempty"`
	ResponseFormat any           `json:"response_format,omitempty"`
	Stream         bool          `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type chatAnswer struct {
	Content          string
	FinishReason     string
	PromptTokens     int
	CompletionTokens int
}

// body marshals req and merges extra over it. Values stay raw JSON so the
// schema keeps its property order (the order the model is asked to write).
func (req chatRequest) body(extra map[string]any) ([]byte, error) {
	b, err := json.Marshal(req)
	if err != nil || len(extra) == 0 {
		return b, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range extra {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("extra %q: %w", k, err)
		}
		m[k] = raw
	}
	return json.Marshal(m)
}

func (c *chatClient) complete(ctx context.Context, req chatRequest, extra map[string]any) (chatAnswer, error) {
	body, err := req.body(extra)
	if err != nil {
		return chatAnswer{}, fmt.Errorf("encode request: %w", err)
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.baseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return chatAnswer{}, err
	}
	hr.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		hr.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(hr)
	if err != nil {
		return chatAnswer{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return chatAnswer{}, err
	}
	var cr chatResponse
	jsonErr := json.Unmarshal(raw, &cr)
	if resp.StatusCode != http.StatusOK {
		if jsonErr == nil && cr.Error != nil && cr.Error.Message != "" {
			return chatAnswer{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, cr.Error.Message)
		}
		return chatAnswer{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(string(raw)))
	}
	if jsonErr != nil {
		return chatAnswer{}, fmt.Errorf("decode response: %w", jsonErr)
	}
	if len(cr.Choices) == 0 {
		return chatAnswer{}, fmt.Errorf("response without choices: %s", snippet(string(raw)))
	}
	return chatAnswer{
		Content:          cr.Choices[0].Message.Content,
		FinishReason:     cr.Choices[0].FinishReason,
		PromptTokens:     cr.Usage.PromptTokens,
		CompletionTokens: cr.Usage.CompletionTokens,
	}, nil
}

func snippet(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
