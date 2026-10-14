package llm

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

// Ollama — локальная модель через Ollama (http://127.0.0.1:11434). Бесплатно,
// переписки не покидают компьютер. Effort игнорируется.
type Ollama struct {
	URL    string
	Model  string
	NumCtx int
	Think  bool // режим рассуждений: медленнее, но точнее держит правила
	http   *http.Client
}

func NewOllama(url, model string, numCtx int) *Ollama {
	if url == "" {
		url = "http://127.0.0.1:11434"
	}
	if numCtx == 0 {
		numCtx = 16384
	}
	return &Ollama{URL: strings.TrimRight(url, "/"), Model: model, NumCtx: numCtx, http: &http.Client{Timeout: 10 * time.Minute}}
}

type ollamaMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaReq struct {
	Model     string         `json:"model"`
	Messages  []ollamaMsg    `json:"messages"`
	Stream    bool           `json:"stream"`
	Format    any            `json:"format,omitempty"`
	Think     bool           `json:"think"`
	KeepAlive string         `json:"keep_alive"`
	Options   map[string]any `json:"options"`
}

type ollamaResp struct {
	Message ollamaMsg `json:"message"`
	Error   string    `json:"error"`
}

func (o *Ollama) Complete(ctx context.Context, r Request) (string, error) {
	req := ollamaReq{
		Model: o.Model,
		Messages: []ollamaMsg{
			{Role: "system", Content: r.System},
			{Role: "user", Content: r.User},
		},
		Think:     o.Think,
		KeepAlive: "30m",
		Options:   map[string]any{"num_ctx": o.NumCtx, "temperature": 0.7},
	}
	if r.Schema != nil {
		req.Format = r.Schema // структурированный ответ строго по JSON-схеме
	}
	if r.MaxTokens > 0 {
		req.Options["num_predict"] = r.MaxTokens
	}
	body, _ := json.Marshal(req)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, o.URL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := o.http.Do(hr)
	if err != nil {
		return "", fmt.Errorf("ollama недоступна (%s): %w", o.URL, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out ollamaResp
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("ollama: %s: %s", resp.Status, data)
	}
	if out.Error != "" || resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama: %s %s", resp.Status, out.Error)
	}
	return strings.TrimSpace(stripThink(out.Message.Content)), nil
}

// stripThink убирает рассуждения <think>…</think>, если модель их всё же выдала.
func stripThink(s string) string {
	for {
		i := strings.Index(s, "<think>")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "</think>")
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+len("</think>"):]
	}
}
