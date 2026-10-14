// Package recap — обязательный ежедневный рекап переписок.
package recap

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SaynoMore22/tg-autoreply/internal/llm"
	"github.com/SaynoMore22/tg-autoreply/internal/store"
	"github.com/SaynoMore22/tg-autoreply/internal/tgx"
)

const (
	keyCutoff = "recap_cutoff" // unix-время, до которого всё уже вошло в рекап
	keyDate   = "recap_date"   // дата последнего планового рекапа
)

type Recapper struct {
	Store  *store.Store
	LLM    llm.Model
	TG     tgx.Messenger
	Effort string
	Owner  string
	Dir    string // куда класть recaps/*.md
	At     string // "23:30"
	DryRun bool
}

// Loop: при старте догоняет пропущенное, дальше раз в минуту проверяет время рекапа.
func (r *Recapper) Loop(ctx context.Context) {
	r.catchUp(ctx)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tick(ctx)
		}
	}
}

func (r *Recapper) scheduled(day time.Time) time.Time {
	h, m := 23, 30
	if parts := strings.Split(r.At, ":"); len(parts) == 2 {
		if v, err := strconv.Atoi(parts[0]); err == nil {
			h = v
		}
		if v, err := strconv.Atoi(parts[1]); err == nil {
			m = v
		}
	}
	y, mo, d := day.Date()
	return time.Date(y, mo, d, h, m, 0, 0, day.Location())
}

func (r *Recapper) cutoff() time.Time {
	v, _ := r.Store.Get(keyCutoff)
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Unix(n, 0)
	}
	return time.Time{}
}

func (r *Recapper) tick(ctx context.Context) {
	now := time.Now()
	today := now.Format("2006-01-02")
	if last, _ := r.Store.Get(keyDate); last == today {
		return
	}
	if now.Before(r.scheduled(now)) {
		return
	}
	if err := r.Run(ctx, r.cutoff(), now, "Рекап за "+now.Format("02.01")); err != nil {
		log.Printf("рекап: %v", err)
		return
	}
	_ = r.Store.Set(keyDate, today)
}

// catchUp делает рекап того, что накопилось, пока бот был выключен в момент планового рекапа.
func (r *Recapper) catchUp(ctx context.Context) {
	cut := r.cutoff()
	now := time.Now()
	if cut.IsZero() {
		_ = r.Store.Set(keyCutoff, strconv.FormatInt(now.Unix(), 10))
		return
	}
	// Последний плановый момент, который должен был случиться.
	due := r.scheduled(now)
	if now.Before(due) {
		due = r.scheduled(now.AddDate(0, 0, -1))
	}
	if !cut.Before(due) {
		return
	}
	if err := r.Run(ctx, cut, due, "Пропущенный рекап до "+due.Format("02.01 15:04")); err != nil {
		log.Printf("рекап (догонялка): %v", err)
		return
	}
	if due.Format("2006-01-02") == now.Format("2006-01-02") {
		_ = r.Store.Set(keyDate, now.Format("2006-01-02"))
	}
}

// Run собирает рекап за (from, to], отправляет в «Избранное» и сохраняет в файл.
func (r *Recapper) Run(ctx context.Context, from, to time.Time, title string) error {
	msgs, err := r.Store.MessagesBetween(from, to)
	if err != nil {
		return err
	}
	events, err := r.Store.EventsBetween(from, to)
	if err != nil {
		return err
	}

	var text string
	if len(msgs) == 0 && len(events) == 0 {
		text = "🗒 " + title + "\nПереписок не было."
	} else {
		body, err := r.LLM.Complete(ctx, llm.Request{
			System: "Ты составляешь владельцу Telegram-аккаунта короткий вечерний рекап его переписок. Пиши по-русски, простым текстом без markdown (Telegram его не отрисует), можно эмодзи как маркеры. Только факты из переписки.",
			User:   prompt(r.Owner, msgs, events),
			Effort: r.Effort,
		})
		if err != nil {
			return err
		}
		text = "🗒 " + title + "\n\n" + body
	}

	if err := os.MkdirAll(filepath.Join(r.Dir, "recaps"), 0o700); err == nil {
		name := filepath.Join(r.Dir, "recaps", to.Format("2006-01-02_1504")+".md")
		_ = os.WriteFile(name, []byte(text), 0o600)
	}
	if r.DryRun {
		log.Print(text)
	} else if err := r.TG.NotifyOwner(ctx, text); err != nil {
		return err
	}
	return r.Store.Set(keyCutoff, strconv.FormatInt(to.Unix(), 10))
}

func prompt(owner string, msgs []store.Message, events []store.Event) string {
	byChat := map[string][]store.Message{}
	var order []string
	for _, m := range msgs {
		if _, ok := byChat[m.ChatKey]; !ok {
			order = append(order, m.ChatKey)
		}
		byChat[m.ChatKey] = append(byChat[m.ChatKey], m)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Владелец: %s. Реплики «Я (бот)» за него написал автоответчик, «Я» — он сам.\n\n", owner)
	for _, k := range order {
		list := byChat[k]
		fmt.Fprintf(&sb, "=== %s\n", list[0].ChatTitle)
		for _, m := range list {
			fmt.Fprintf(&sb, "[%s] %s: %s\n", m.At.Format("15:04"), m.Sender, m.Text)
		}
		sb.WriteString("\n")
	}
	if len(events) > 0 {
		sb.WriteString("=== События автоответчика\n")
		for _, e := range events {
			fmt.Fprintf(&sb, "[%s] %s — %s: %s\n", e.At.Format("15:04"), e.ChatTitle, e.Kind, e.Note)
		}
	}
	sb.WriteString(`
Сделай рекап:
1. По каждому чату 1-2 строки: о чём говорили и чем закончилось.
2. «Что бот ответил за тебя» — только то, что важно знать (о чём договорился, что сказал по сути).
3. «Ждут твоего ответа» — вопросы без ответа, отложенные ботом темы (defer), остановки по лимитам.
4. Важное и срочное, если есть.
Коротко, без воды.`)
	return sb.String()
}
