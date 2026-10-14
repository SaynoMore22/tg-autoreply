// Package simtg — Telegram в памяти для симулятора: ничего не уходит в сеть.
package simtg

import (
	"context"
	"sync"
	"time"

	"github.com/gotd/td/tg"

	"github.com/SaynoMore22/tg-autoreply/internal/tgx"
)

type Event struct {
	Kind string // send, react, notify
	Chat string
	Text string
	At   time.Time
}

type TG struct {
	mu      sync.Mutex
	history map[string][]tgx.Line
	events  []Event
	nextID  int
}

func New() *TG { return &TG{history: map[string][]tgx.Line{}, nextID: 1} }

func (f *TG) NewID() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	return f.nextID
}

func (f *TG) AddLine(chat string, l tgx.Line) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.history[chat] = append(f.history[chat], l)
}

func (f *TG) Events() []Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Event(nil), f.events...)
}

func (f *TG) History(_ context.Context, c tgx.Chat, limit, offsetID int) ([]tgx.Line, error) {
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

func (f *TG) Send(_ context.Context, c tgx.Chat, text string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.history[c.Key] = append(f.history[c.Key], tgx.Line{ID: f.nextID, Out: true, Text: text, At: time.Now()})
	f.events = append(f.events, Event{"send", c.Key, text, time.Now()})
	return nil
}

func (f *TG) React(_ context.Context, c tgx.Chat, _ int, emoji string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, Event{"react", c.Key, emoji, time.Now()})
	return nil
}

func (f *TG) NotifyOwner(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, Event{"notify", "", text, time.Now()})
	return nil
}

func (f *TG) Typing(context.Context, tgx.Chat)        {}
func (f *TG) MarkRead(context.Context, tgx.Chat, int) {}
func (f *TG) Dialogs(context.Context, int) (tg.Entities, error) {
	return tg.Entities{Users: map[int64]*tg.User{}}, nil
}

func (f *TG) ArchivedKeys(context.Context) (map[string]bool, error) { return map[string]bool{}, nil }
