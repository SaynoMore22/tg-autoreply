package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"github.com/SaynoMore22/tg-autoreply/internal/config"
	"github.com/SaynoMore22/tg-autoreply/internal/llm"
	"github.com/SaynoMore22/tg-autoreply/internal/privacy"
	"github.com/SaynoMore22/tg-autoreply/internal/store"
	"github.com/SaynoMore22/tg-autoreply/internal/style"
	"github.com/SaynoMore22/tg-autoreply/internal/tgx"
)

// ---------- фейковый Telegram ----------

type sentMsg struct {
	Chat    string
	Text    string
	ReplyTo int
}

type reaction struct {
	Chat  string
	MsgID int
	Emoji string
}

type fakeTG struct {
	mu        sync.Mutex
	history   map[string][]tgx.Line
	sent      []sentMsg
	reactions []reaction
	notes     []string
	reads     int
	nextID    int
	onSend    func(chat tgx.Chat, text string) // имитация эхо-апдейта
	archived  map[string]bool
}

func newFakeTG() *fakeTG { return &fakeTG{history: map[string][]tgx.Line{}, nextID: 1000} }

func (f *fakeTG) newID() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	return f.nextID
}

func (f *fakeTG) addLine(chat string, l tgx.Line) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.history[chat] = append(f.history[chat], l)
}

func (f *fakeTG) History(_ context.Context, c tgx.Chat, limit, offsetID int) ([]tgx.Line, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []tgx.Line
	for _, l := range f.history[c.Key] {
		if offsetID == 0 || l.ID < offsetID {
			all = append(all, l)
		}
	}
	if len(all) > limit {
		all = all[len(all)-limit:]
	}
	return append([]tgx.Line(nil), all...), nil
}

func (f *fakeTG) Send(_ context.Context, c tgx.Chat, text string, replyTo int) error {
	f.mu.Lock()
	f.nextID++
	id := f.nextID
	f.sent = append(f.sent, sentMsg{Chat: c.Key, Text: text, ReplyTo: replyTo})
	f.history[c.Key] = append(f.history[c.Key], tgx.Line{ID: id, Out: true, Text: text, At: time.Now()})
	hook := f.onSend
	f.mu.Unlock()
	if hook != nil {
		hook(c, text)
	}
	return nil
}

func (f *fakeTG) React(_ context.Context, c tgx.Chat, msgID int, emoji string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactions = append(f.reactions, reaction{c.Key, msgID, emoji})
	return nil
}

func (f *fakeTG) Typing(context.Context, tgx.Chat) {}

func (f *fakeTG) MarkRead(context.Context, tgx.Chat, int) {
	f.mu.Lock()
	f.reads++
	f.mu.Unlock()
}

func (f *fakeTG) NotifyOwner(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, text)
	return nil
}

func (f *fakeTG) Dialogs(context.Context, int) (tg.Entities, error) {
	return tg.Entities{Users: map[int64]*tg.User{}}, nil
}

func (f *fakeTG) ArchivedKeys(context.Context) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]bool{}
	for k := range f.archived {
		out[k] = true
	}
	return out, nil
}

func (f *fakeTG) Sent() []sentMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMsg(nil), f.sent...)
}

func (f *fakeTG) Notes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.notes...)
}

func (f *fakeTG) Reactions() []reaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]reaction(nil), f.reactions...)
}

// ---------- фейковая модель ----------

type fakeModel struct {
	mu      sync.Mutex
	calls   []llm.Request
	delay   time.Duration
	respond func(n int, r llm.Request) (string, error) // n — номер вызова с 1
}

func (m *fakeModel) Complete(ctx context.Context, r llm.Request) (string, error) {
	m.mu.Lock()
	m.calls = append(m.calls, r)
	n := len(m.calls)
	m.mu.Unlock()
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if m.respond == nil {
		return reply("ок"), nil
	}
	return m.respond(n, r)
}

// replyCalls — только вызовы для ответов (не построение профилей).
func (m *fakeModel) replyCalls() []llm.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []llm.Request
	for _, c := range m.calls {
		if c.Schema != nil {
			out = append(out, c)
		}
	}
	return out
}

func decisionJSON(action string, msgs []string, react, note string) string {
	if msgs == nil {
		msgs = []string{}
	}
	b, _ := json.Marshal(decision{Action: action, Messages: msgs, Reaction: react, OwnerNote: note})
	return string(b)
}

func reply(msgs ...string) string { return decisionJSON("reply", msgs, "", "") }

// ---------- сборка бота ----------

type env struct {
	t     *testing.T
	b     *Bot
	tg    *fakeTG
	model *fakeModel
	st    *store.Store
	ctx   context.Context
	msgID int
}

func fastConfig() config.Config {
	cfg := config.Default()
	cfg.DebounceSeconds = 0.08
	cfg.MaxWaitSeconds = 0.6
	cfg.TypingExtendSeconds = 0.15
	cfg.TypingCharsPerSecond = 1e9
	cfg.MaxTypingSeconds = 0
	cfg.PauseBetweenPartsSeconds = 0
	cfg.FloodMessages = 12
	cfg.FloodWindowSeconds = 60
	cfg.ReactionWithReplyChance = 1
	cfg.StrangerMode = "reply"
	return cfg
}

func newEnv(t *testing.T, cfg config.Config, model *fakeModel, corpus *style.Corpus, persona Persona, dry bool) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := store.Open(filepath.Join(t.TempDir(), "log.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := newFakeTG()
	if model == nil {
		model = &fakeModel{}
	}
	guard := privacy.NewGuard(cfg.AllowLinks)
	b := New(ctx, cfg, f, model, st, 1, persona, "общий профиль: пишет коротко", corpus, guard, dry)
	return &env{t: t, b: b, tg: f, model: model, st: st, ctx: ctx, msgID: 100}
}

func user(id int64, name string, contact bool) *tg.User {
	return &tg.User{ID: id, AccessHash: id * 7, FirstName: name, Contact: contact}
}

// in — входящее личное сообщение от u.
func (e *env) in(u *tg.User, text string) int {
	id := e.tg.newID()
	key := fmt.Sprintf("u%d", u.ID)
	e.tg.addLine(key, tgx.Line{ID: id, Sender: u.FirstName, Text: text, At: time.Now()})
	e.b.OnMessage(e.ctx, tg.Entities{Users: map[int64]*tg.User{u.ID: u}}, &tg.Message{
		ID: id, PeerID: &tg.PeerUser{UserID: u.ID}, FromID: &tg.PeerUser{UserID: u.ID}, Message: text,
	})
	return id
}

// out — сообщение, которое владелец написал сам.
func (e *env) out(u *tg.User, text string) {
	id := e.tg.newID()
	key := fmt.Sprintf("u%d", u.ID)
	e.tg.addLine(key, tgx.Line{ID: id, Out: true, Text: text, At: time.Now()})
	e.b.OnMessage(e.ctx, tg.Entities{Users: map[int64]*tg.User{u.ID: u}}, &tg.Message{
		ID: id, Out: true, PeerID: &tg.PeerUser{UserID: u.ID}, Message: text,
	})
}

// group — сообщение в группе.
func (e *env) group(chatID int64, from *tg.User, text string, mentioned bool) int {
	id := e.tg.newID()
	key := fmt.Sprintf("c%d", chatID)
	e.tg.addLine(key, tgx.Line{ID: id, Sender: from.FirstName, Text: text, At: time.Now()})
	m := &tg.Message{ID: id, PeerID: &tg.PeerChat{ChatID: chatID}, FromID: &tg.PeerUser{UserID: from.ID}, Message: text}
	m.SetMentioned(mentioned)
	e.b.OnMessage(e.ctx, tg.Entities{
		Users: map[int64]*tg.User{from.ID: from},
		Chats: map[int64]*tg.Chat{chatID: {ID: chatID, Title: "Группа"}},
	}, m)
	return id
}

// settle ждёт, пока бот закончит всю работу (таймеры + обработка).
func (e *env) settle() {
	e.t.Helper()
	time.Sleep(time.Duration(e.b.cfg.MaxWaitSeconds*float64(time.Second)) + 150*time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.b.mu.Lock()
		busy := false
		for _, st := range e.b.chats {
			if st.running {
				busy = true
			}
		}
		e.b.mu.Unlock()
		if !busy {
			time.Sleep(50 * time.Millisecond)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatal("бот не закончил работу за 5 секунд")
}

func contains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

var errBoom = errors.New("boom")
