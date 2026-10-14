package recap

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"github.com/SaynoMore22/tg-autoreply/internal/llm"
	"github.com/SaynoMore22/tg-autoreply/internal/store"
	"github.com/SaynoMore22/tg-autoreply/internal/tgx"
)

type fakeTG struct {
	mu    sync.Mutex
	notes []string
}

func (f *fakeTG) History(context.Context, tgx.Chat, int, int) ([]tgx.Line, error) { return nil, nil }
func (f *fakeTG) Send(context.Context, tgx.Chat, string, int) error               { return nil }
func (f *fakeTG) React(context.Context, tgx.Chat, int, string) error              { return nil }
func (f *fakeTG) Typing(context.Context, tgx.Chat)                                {}
func (f *fakeTG) MarkRead(context.Context, tgx.Chat, int)                         {}
func (f *fakeTG) Dialogs(context.Context, int) (tg.Entities, error)               { return tg.Entities{}, nil }
func (f *fakeTG) NotifyOwner(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, text)
	return nil
}

type fakeModel struct{ prompts []string }

func (m *fakeModel) Complete(_ context.Context, r llm.Request) (string, error) {
	m.prompts = append(m.prompts, r.User)
	return "сводка", nil
}

func setup(t *testing.T) (*Recapper, *fakeTG, *fakeModel, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "log.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f, m := &fakeTG{}, &fakeModel{}
	return &Recapper{Store: st, LLM: m, TG: f, Owner: "Алексей", Dir: t.TempDir(), At: "23:30"}, f, m, st
}

func TestEmptyDayStillReported(t *testing.T) {
	r, f, m, _ := setup(t)
	if err := r.Run(context.Background(), time.Unix(0, 0), time.Now(), "Рекап"); err != nil {
		t.Fatal(err)
	}
	if len(f.notes) != 1 || !strings.Contains(f.notes[0], "Переписок не было") || len(m.prompts) != 0 {
		t.Fatalf("notes=%q prompts=%d", f.notes, len(m.prompts))
	}
}

func TestRecapCoversOnlyNewAndMarksBot(t *testing.T) {
	r, f, m, st := setup(t)
	old := time.Now().Add(-48 * time.Hour)
	_ = st.AddMessage(store.Message{ChatKey: "u1", ChatTitle: "Петя", Sender: "Петя", Text: "старое", At: old})
	_ = st.Set(keyCutoff, strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10))
	_ = st.AddMessage(store.Message{ChatKey: "u1", ChatTitle: "Петя", Sender: "Петя", Text: "го в кино", At: time.Now()})
	_ = st.AddMessage(store.Message{ChatKey: "u1", ChatTitle: "Петя", Sender: "Я (бот)", Out: true, ByBot: true, Text: "го", At: time.Now()})
	_ = st.AddEvent(store.Event{ChatKey: "u2", ChatTitle: "Маша", Kind: "defer", Note: "просит денег", At: time.Now()})
	if err := r.Run(context.Background(), r.cutoff(), time.Now().Add(time.Second), "Рекап"); err != nil {
		t.Fatal(err)
	}
	p := m.prompts[0]
	if strings.Contains(p, "старое") || !strings.Contains(p, "го в кино") || !strings.Contains(p, "Я (бот)") || !strings.Contains(p, "просит денег") {
		t.Fatalf("prompt:\n%s", p)
	}
	if len(f.notes) != 1 || !strings.Contains(f.notes[0], "сводка") {
		t.Fatalf("notes = %q", f.notes)
	}
	// Повторный рекап не включает уже учтённое.
	_ = r.Run(context.Background(), r.cutoff(), time.Now().Add(2*time.Second), "Рекап")
	if !strings.Contains(f.notes[1], "Переписок не было") {
		t.Fatalf("повторный рекап: %q", f.notes[1])
	}
}

func TestCatchUpAfterDowntime(t *testing.T) {
	r, f, _, st := setup(t)
	r.At = "00:00" // плановое время уже прошло сегодня
	_ = st.Set(keyCutoff, strconv.FormatInt(time.Now().Add(-72*time.Hour).Unix(), 10))
	_ = st.AddMessage(store.Message{ChatKey: "u1", ChatTitle: "Петя", Sender: "Петя", Text: "ау", At: time.Now().Add(-30 * time.Hour)})
	r.catchUp(context.Background())
	if len(f.notes) != 1 || !strings.Contains(f.notes[0], "Пропущенный") {
		t.Fatalf("пропущенный рекап не отправлен: %q", f.notes)
	}
	r.catchUp(context.Background())
	if len(f.notes) != 1 {
		t.Fatal("пропущенный рекап отправлен дважды")
	}
}

func TestFirstStartOnlySetsCutoff(t *testing.T) {
	r, f, _, st := setup(t)
	r.catchUp(context.Background())
	if len(f.notes) != 0 {
		t.Fatal("при первом запуске рекап не нужен")
	}
	if v, _ := st.Get(keyCutoff); v == "" {
		t.Fatal("cutoff не установлен")
	}
}

func TestTickOncePerDay(t *testing.T) {
	r, f, _, _ := setup(t)
	r.At = "00:00"
	r.tick(context.Background())
	r.tick(context.Background())
	if len(f.notes) != 1 {
		t.Fatalf("за день рекапов %d, нужен 1", len(f.notes))
	}
	r2, f2, _, _ := setup(t)
	r2.At = "23:59"
	if time.Now().Hour() < 23 {
		r2.tick(context.Background())
		if len(f2.notes) != 0 {
			t.Fatal("рекап раньше времени")
		}
	}
}

func (f *fakeTG) ArchivedKeys(context.Context) (map[string]bool, error) { return nil, nil }
