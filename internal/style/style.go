// Package style собирает корпус собственных сообщений владельца
// и строит по нему профиль манеры общения.
package style

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gotd/td/tg"

	"github.com/SaynoMore22/tg-autoreply/internal/llm"
	"github.com/SaynoMore22/tg-autoreply/internal/privacy"
	"github.com/SaynoMore22/tg-autoreply/internal/tgx"
)

// Exchange — кусок диалога: реплики собеседника и ответная серия владельца.
type Exchange struct {
	ChatKey string   `json:"chat_key"`
	Chat    string   `json:"chat"`
	Context []string `json:"context"` // "Имя: текст"
	Reply   []string `json:"reply"`   // сообщения владельца подряд
}

type Stats struct {
	Messages          int            `json:"messages"`
	AvgLen            float64        `json:"avg_len_chars"`
	MedianLen         int            `json:"median_len_chars"`
	LowercaseStartPct float64        `json:"lowercase_start_pct"`
	EndsWithDotPct    float64        `json:"ends_with_dot_pct"`
	HasCommaPct       float64        `json:"has_comma_pct"`
	QuestionPct       float64        `json:"question_mark_pct"`
	ExclamationPct    float64        `json:"exclamation_pct"`
	BracketSmilePct   float64        `json:"bracket_smile_pct"` // ")" / "(("
	EmojiPct          float64        `json:"emoji_pct"`
	AvgBurst          float64        `json:"avg_messages_in_a_row"`
	TopEmoji          []string       `json:"top_emoji"`
	TopWords          []string       `json:"top_words"`
	Laughs            map[string]int `json:"laughs"`
}

type Corpus struct {
	CreatedAt time.Time             `json:"created_at"`
	Stats     Stats                 `json:"stats"`
	Exchanges []Exchange            `json:"exchanges"`
	Chats     map[string]*ChatStyle `json:"chats"`
}

const (
	CorpusFile  = "style_corpus.json"
	ProfileFile = "style.md"
)

// Collect проходит по последним личным диалогам и собирает корпус.
// skip — чаты, которые нельзя читать (архив).
func Collect(ctx context.Context, api *tg.Client, selfID int64, chats, perChat int, skip map[string]bool) (*Corpus, error) {
	res, err := api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 100})
	if err != nil {
		return nil, fmt.Errorf("get dialogs: %w", err)
	}
	mod, ok := res.AsModified()
	if !ok {
		return nil, fmt.Errorf("dialogs not modified")
	}
	users := map[int64]*tg.User{}
	for _, u := range mod.GetUsers() {
		if uu, ok := u.(*tg.User); ok {
			users[uu.ID] = uu
		}
	}

	corpus := &Corpus{CreatedAt: time.Now(), Chats: map[string]*ChatStyle{}}
	var own []string
	var bursts []int
	taken := 0
	for _, d := range mod.GetDialogs() {
		if taken >= chats {
			break
		}
		dlg, ok := d.(*tg.Dialog)
		if !ok {
			continue
		}
		pu, ok := dlg.Peer.(*tg.PeerUser)
		if !ok {
			continue
		}
		u := users[pu.UserID]
		if u == nil || u.Bot || u.Self || u.Deleted || u.Support || u.ID == selfID || u.ID == 777000 {
			continue
		}
		if skip[fmt.Sprintf("u%d", u.ID)] {
			continue // архив
		}
		peer := &tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash}
		lines, err := fetch(ctx, api, peer, perChat)
		if err != nil {
			log.Printf("style: чат %s: %v", tgx.UserName(u), err)
			continue
		}
		key := fmt.Sprintf("u%d", u.ID)
		name := tgx.UserName(u)
		ex, o, b := split(key, name, lines)
		if len(o) == 0 {
			continue
		}
		taken++
		corpus.Exchanges = append(corpus.Exchanges, ex...)
		corpus.Chats[key] = &ChatStyle{ChatKey: key, Chat: name, Stats: computeStats(o, b), Exchanges: ex}
		own = append(own, o...)
		bursts = append(bursts, b...)
		log.Printf("style: %s — %d своих сообщений", name, len(o))
	}
	corpus.Stats = computeStats(own, bursts)
	return corpus, nil
}

func fetch(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, total int) ([]tgx.Line, error) {
	var all []tgx.Line
	offset := 0
	for len(all) < total {
		batch, err := tgx.History(ctx, api, peer, 100, offset)
		if err != nil {
			return all, err
		}
		if len(batch) == 0 {
			break
		}
		all = append(batch, all...)
		offset = batch[0].ID
		if len(batch) < 100 {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	return all, nil
}

// split режет историю на обмены «собеседник → серия владельца».
func split(key, chat string, lines []tgx.Line) (ex []Exchange, own []string, bursts []int) {
	i := 0
	for i < len(lines) {
		if !lines[i].Out {
			i++
			continue
		}
		start := i
		var reply []string
		for i < len(lines) && lines[i].Out {
			if lines[i].Text != "" && !strings.HasPrefix(lines[i].Text, "[") {
				own = append(own, lines[i].Text)
			}
			reply = append(reply, lines[i].Text)
			i++
		}
		bursts = append(bursts, len(reply))
		var ctxLines []string
		for j := max(0, start-4); j < start; j++ {
			who := lines[j].Sender
			if lines[j].Out {
				who = "Я"
			}
			ctxLines = append(ctxLines, who+": "+lines[j].Text)
		}
		if len(ctxLines) > 0 {
			ex = append(ex, Exchange{ChatKey: key, Chat: chat, Context: ctxLines, Reply: reply})
		}
	}
	return
}

func isEmoji(r rune) bool {
	return (r >= 0x1F300 && r <= 0x1FAFF) || (r >= 0x2600 && r <= 0x27BF)
}

func computeStats(msgs []string, bursts []int) Stats {
	s := Stats{Messages: len(msgs), Laughs: map[string]int{}}
	if len(msgs) == 0 {
		return s
	}
	lens := make([]int, 0, len(msgs))
	emoji := map[string]int{}
	words := map[string]int{}
	var total, lower, dot, comma, q, excl, br, em int
	for _, m := range msgs {
		n := utf8.RuneCountInString(m)
		lens = append(lens, n)
		total += n
		first, _ := utf8.DecodeRuneInString(m)
		if unicode.IsLetter(first) && unicode.IsLower(first) {
			lower++
		}
		t := strings.TrimSpace(m)
		if strings.HasSuffix(t, ".") && !strings.HasSuffix(t, "..") {
			dot++
		}
		if strings.Contains(m, ",") {
			comma++
		}
		if strings.Contains(m, "?") {
			q++
		}
		if strings.Contains(m, "!") {
			excl++
		}
		if strings.Contains(m, ")") || strings.Contains(m, "((") {
			br++
		}
		hasEm := false
		for _, r := range m {
			if isEmoji(r) {
				emoji[string(r)]++
				hasEm = true
			}
		}
		if hasEm {
			em++
		}
		for _, w := range strings.FieldsFunc(strings.ToLower(m), func(r rune) bool { return !unicode.IsLetter(r) }) {
			if utf8.RuneCountInString(w) >= 2 {
				words[w]++
			}
			if strings.HasPrefix(w, "ах") || strings.HasPrefix(w, "ха") || strings.HasPrefix(w, "хх") || strings.HasPrefix(w, "пх") || w == "лол" || w == "кек" || w == "ору" {
				s.Laughs[w]++
			}
		}
	}
	sort.Ints(lens)
	pct := func(x int) float64 { return float64(x) * 100 / float64(len(msgs)) }
	s.AvgLen = float64(total) / float64(len(msgs))
	s.MedianLen = lens[len(lens)/2]
	s.LowercaseStartPct = pct(lower)
	s.EndsWithDotPct = pct(dot)
	s.HasCommaPct = pct(comma)
	s.QuestionPct = pct(q)
	s.ExclamationPct = pct(excl)
	s.BracketSmilePct = pct(br)
	s.EmojiPct = pct(em)
	if len(bursts) > 0 {
		sum := 0
		for _, b := range bursts {
			sum += b
		}
		s.AvgBurst = float64(sum) / float64(len(bursts))
	}
	s.TopEmoji = top(emoji, 15)
	s.TopWords = top(words, 80)
	return s
}

func top(m map[string]int, n int) []string {
	type kv struct {
		k string
		v int
	}
	var list []kv
	for k, v := range m {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })
	var out []string
	for i := 0; i < len(list) && i < n; i++ {
		out = append(out, fmt.Sprintf("%s(%d)", list[i].k, list[i].v))
	}
	return out
}

// BuildProfile просит модель описать манеру общения по корпусу.
func BuildProfile(ctx context.Context, c llm.Model, effort, owner string, corpus *Corpus) (string, error) {
	stats, _ := json.MarshalIndent(corpus.Stats, "", "  ")
	sample := Redacted(Sample(corpus.Exchanges, 700))
	var sb strings.Builder
	for _, e := range sample {
		sb.WriteString(FormatExchange(e, true))
		sb.WriteString("\n")
	}
	system := `Ты лингвист, который составляет точный портрет того, как конкретный человек пишет в мессенджере. Портрет потом используется, чтобы отвечать его голосом так, чтобы собеседники не заметили разницы. Опирайся только на материал, ничего не приукрашивай и не выдумывай.`
	user := fmt.Sprintf(`Владелец аккаунта: %s. Ниже статистика по его сообщениям и реальные фрагменты переписок (строки "Я:" и блок "ОТВЕТ" — это он).

СТАТИСТИКА:
%s

ФРАГМЕНТЫ:
%s

Составь профиль стиля на русском в markdown. Разделы:
1. Общий тон и темперамент.
2. Длина сообщений и как дробит мысль на несколько сообщений подряд.
3. Регистр, пунктуация, опечатки, сокращения (конкретно: ставит ли точки, запятые, заглавные).
4. Словарь: частые слова, сленг, мат (насколько часто и какой), англицизмы, слова-паразиты.
5. Смех, эмодзи, скобочки, стикеры — что и когда.
6. Типовые конструкции фраз — 20-30 шаблонов с примерами из материала.
7. Как отвечает на: вопросы, шутки, просьбы, новости, жалобы, приветствия и прощания.
8. Различия в общении с людьми разного типа (близкие друзья, семья, коллеги, малознакомые) — БЕЗ имён.
9. Чего он НИКОГДА не делает в переписке (например, длинные вежливые формулировки, смайлы определённого типа).
10. 40 коротких характерных реплик дословно из материала — только нейтральные, без имён, мест, дат, планов и личных фактов.

КОНФИДЕНЦИАЛЬНОСТЬ: профиль описывает ТОЛЬКО манеру речи. Не упоминай имена людей, города, адреса, работу, учёбу, события, планы, отношения, здоровье, деньги и любые другие факты из переписок. Если пример реплики содержит такие факты — возьми другой или обезличь.
Пиши конкретно, с примерами, без общих слов.`, owner, stats, sb.String())
	profile, err := c.Complete(ctx, llm.Request{System: system, User: user, Effort: effort, MaxTokens: 16000})
	return privacy.Redact(profile), err
}

func Sample(ex []Exchange, n int) []Exchange {
	if len(ex) <= n {
		return ex
	}
	idx := rand.Perm(len(ex))[:n]
	sort.Ints(idx)
	out := make([]Exchange, 0, n)
	for _, i := range idx {
		out = append(out, ex[i])
	}
	return out
}

// FormatExchange печатает обмен. anon=true — без названия чата и имён собеседников
// (для общих примеров, которые модель видит при ответе любому человеку).
func FormatExchange(e Exchange, anon bool) string {
	var sb strings.Builder
	if anon {
		sb.WriteString("---\n")
	} else {
		fmt.Fprintf(&sb, "--- чат: %s\n", e.Chat)
	}
	for _, l := range e.Context {
		if anon && !strings.HasPrefix(l, "Я: ") {
			if i := strings.Index(l, ": "); i >= 0 {
				l = "Собеседник: " + l[i+2:]
			}
		}
		sb.WriteString(l + "\n")
	}
	sb.WriteString("ОТВЕТ:\n")
	for _, r := range e.Reply {
		sb.WriteString("  " + r + "\n")
	}
	return sb.String()
}

// Redacted возвращает копию обменов с вычищенными телефонами, почтами, картами, ссылками и т.п.
func Redacted(ex []Exchange) []Exchange {
	out := make([]Exchange, len(ex))
	for i, e := range ex {
		c := Exchange{ChatKey: e.ChatKey, Chat: e.Chat}
		for _, l := range e.Context {
			c.Context = append(c.Context, privacy.Redact(l))
		}
		for _, r := range e.Reply {
			c.Reply = append(c.Reply, privacy.Redact(r))
		}
		out[i] = c
	}
	return out
}

func Save(dir string, corpus *Corpus, profile string) error {
	data, err := json.Marshal(corpus)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, CorpusFile), data, 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ProfileFile), []byte(profile), 0o600)
}

func Load(dir string) (*Corpus, string, error) {
	data, err := os.ReadFile(filepath.Join(dir, CorpusFile))
	if err != nil {
		return nil, "", err
	}
	var c Corpus
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, "", err
	}
	profile, err := os.ReadFile(filepath.Join(dir, ProfileFile))
	if err != nil {
		return nil, "", err
	}
	return &c, string(profile), nil
}
