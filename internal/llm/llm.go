package llm

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

// ErrRefused — модель (и цепочка фолбэков) отказалась отвечать.
var ErrRefused = errors.New("model refused")

type Client struct {
	api   anthropic.Client
	model string
}

// New использует ANTHROPIC_API_KEY из окружения.
func New(model string) *Client {
	return &Client{api: anthropic.NewClient(), model: model}
}

type Request struct {
	System    string // стабильная часть, кешируется
	User      string // переменная часть
	Effort    string // low | medium | high | xhigh | max
	MaxTokens int64
	Schema    map[string]any // если задан — ответ строго JSON по схеме
}

// Complete делает один запрос и возвращает текст ответа.
// Включены серверные фолбэки: при отказе модели запрос автоматически
// переотправляется на подходящую модель внутри того же вызова.
func (c *Client) Complete(ctx context.Context, r Request) (string, error) {
	if r.MaxTokens == 0 {
		r.MaxTokens = 16000
	}
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: r.MaxTokens,
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
		System: []anthropic.BetaTextBlockParam{{
			Text:         r.System,
			CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		}},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(r.User)),
		},
	}
	oc := anthropic.BetaOutputConfigParam{}
	if r.Effort != "" {
		oc.Effort = anthropic.BetaOutputConfigEffort(r.Effort)
	}
	if r.Schema != nil {
		oc.Format = anthropic.BetaJSONOutputFormatParam{Schema: r.Schema}
	}
	if !param.IsOmitted(oc) {
		params.OutputConfig = oc
	}

	resp, err := c.api.Beta.Messages.New(ctx, params)
	if err != nil {
		return "", err
	}
	if resp.StopReason == anthropic.BetaStopReasonRefusal {
		return "", ErrRefused
	}
	var sb strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			sb.WriteString(t.Text)
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

// Model — всё, что боту нужно от языковой модели (в тестах — фейк).
type Model interface {
	Complete(ctx context.Context, r Request) (string, error)
}

// Open выбирает модель по настройкам: "ollama" — локальная, иначе Claude API.
func Open(provider, claudeModel, ollamaURL, ollamaModel string, numCtx int, think bool) (Model, error) {
	switch provider {
	case "ollama":
		o := NewOllama(ollamaURL, ollamaModel, numCtx)
		o.Think = think
		return o, nil
	case "", "anthropic":
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			return nil, errors.New("для provider=anthropic нужен ANTHROPIC_API_KEY в .env")
		}
		return New(claudeModel), nil
	}
	return nil, errors.New("неизвестный provider: " + provider + " (ollama или anthropic)")
}
