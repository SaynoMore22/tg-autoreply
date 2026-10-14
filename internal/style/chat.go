package style

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SaynoMore22/tg-autoreply/internal/llm"
	"github.com/SaynoMore22/tg-autoreply/internal/privacy"
	"github.com/SaynoMore22/tg-autoreply/internal/tgx"
)

// ChatStyle — манера общения владельца с конкретным человеком.
// С разными людьми он пишет по-разному, поэтому каждый чат анализируется отдельно.
type ChatStyle struct {
	ChatKey   string     `json:"chat_key"`
	Chat      string     `json:"chat"`
	Stats     Stats      `json:"stats"`
	Profile   string     `json:"profile"`
	Exchanges []Exchange `json:"exchanges"`
}

// MinOwnMessages — сколько своих сообщений нужно, чтобы строить отдельный профиль чата.
const MinOwnMessages = 20

// FromHistory разбирает историю одного чата (для чатов, которых не было в корпусе).
func FromHistory(key, name string, lines []tgx.Line) *ChatStyle {
	ex, own, bursts := split(key, name, lines)
	return &ChatStyle{ChatKey: key, Chat: name, Stats: computeStats(own, bursts), Exchanges: ex}
}

// StatsLine — компактная сводка для промпта.
func StatsLine(s Stats) string {
	if s.Messages == 0 {
		return "нет данных"
	}
	return fmt.Sprintf("его сообщений: %d; средняя длина %.0f симв.; с маленькой буквы %.0f%%; точка в конце %.0f%%; эмодзи %.0f%%; скобочки %.0f%%; подряд в среднем %.1f; смех: %v",
		s.Messages, s.AvgLen, s.LowercaseStartPct, s.EndsWithDotPct, s.EmojiPct, s.BracketSmilePct, s.AvgBurst, s.Laughs)
}

// BuildChatProfile описывает, как владелец общается именно с этим человеком.
func BuildChatProfile(ctx context.Context, m llm.Model, effort, owner, general string, cs *ChatStyle) (string, error) {
	stats, _ := json.Marshal(cs.Stats)
	var sb strings.Builder
	sample := Redacted(Sample(cs.Exchanges, 250))
	for _, e := range sample {
		sb.WriteString(FormatExchange(e, false))
	}
	user := fmt.Sprintf(`Владелец: %s. Ниже его общий профиль стиля, статистика и фрагменты переписки с ОДНИМ конкретным человеком (чат «%s»; «Я» и «ОТВЕТ» — владелец).

ОБЩИЙ ПРОФИЛЬ:
%s

СТАТИСТИКА ПО ЭТОМУ ЧАТУ:
%s

ФРАГМЕНТЫ:
%s

Опиши до 250 слов, как он пишет ИМЕННО этому человеку и чем это отличается от общей манеры:
- кто они друг другу по ощущению (близкий друг, семья, коллега, малознакомый) и дистанция;
- как он к нему обращается, прозвища, приветствия;
- тон, мат, подколы, смех, эмодзи, длина и дробление сообщений;
- 8-12 коротких характерных реплик из этого чата дословно.
Не пересказывай содержание переписки и не перечисляй факты (адреса, планы, деньги, здоровье, третьих лиц) — только манеру.`,
		owner, cs.Chat, strings.TrimSpace(general), stats, sb.String())
	out, err := m.Complete(ctx, llm.Request{
		System:    "Ты лингвист и точно описываешь, как человек переписывается с конкретным собеседником. Только манера речи, без фактов из переписки.",
		User:      user,
		Effort:    effort,
		MaxTokens: 4000,
	})
	return privacy.Redact(out), err
}

// Without убирает из корпуса чаты из skip (например, если их убрали в архив
// уже после сбора стиля): ни их профиля, ни примеров не останется в промпте.
func (c *Corpus) Without(skip map[string]bool) {
	if c == nil || len(skip) == 0 {
		return
	}
	kept := c.Exchanges[:0]
	for _, e := range c.Exchanges {
		if !skip[e.ChatKey] {
			kept = append(kept, e)
		}
	}
	c.Exchanges = kept
	for k := range c.Chats {
		if skip[k] {
			delete(c.Chats, k)
		}
	}
}
