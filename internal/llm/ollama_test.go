package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOllamaRequestAndResponse(t *testing.T) {
	var got ollamaReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"<think>хм</think>{\"action\":\"ignore\"}"}}`))
	}))
	defer srv.Close()

	o := NewOllama(srv.URL, "qwen3:14b", 0)
	out, err := o.Complete(context.Background(), Request{System: "sys", User: "usr", Schema: map[string]any{"type": "object"}, MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"action":"ignore"}` {
		t.Fatalf("out = %q", out)
	}
	if got.Model != "qwen3:14b" || got.Stream || got.Think || got.Format == nil ||
		got.Messages[0].Role != "system" || got.Messages[1].Content != "usr" || got.Options["num_ctx"].(float64) != 16384 {
		t.Fatalf("запрос = %+v", got)
	}
}

func TestOllamaErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"model 'x' not found"}`))
	}))
	defer srv.Close()
	if _, err := NewOllama(srv.URL, "x", 0).Complete(context.Background(), Request{}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewOllama("http://127.0.0.1:1", "x", 0).Complete(context.Background(), Request{}); err == nil || !strings.Contains(err.Error(), "недоступна") {
		t.Fatalf("err = %v", err)
	}
}
