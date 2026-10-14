package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	_ = s.AddMessage(Message{ChatKey: "u1", ChatTitle: "Петя", Sender: "Я (бот)", Out: true, ByBot: true, Text: "ку", At: now})
	_ = s.AddMessage(Message{ChatKey: "u1", ChatTitle: "Петя", Sender: "Петя", Text: "старое", At: now.Add(-time.Hour)})
	got, err := s.MessagesBetween(now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil || len(got) != 1 || !got[0].Out || !got[0].ByBot || got[0].Text != "ку" {
		t.Fatalf("got %+v err %v", got, err)
	}
	_ = s.Set("k", "1")
	_ = s.Set("k", "2")
	if v, _ := s.Get("k"); v != "2" {
		t.Fatalf("kv = %q", v)
	}
	if v, _ := s.Get("нет"); v != "" {
		t.Fatalf("missing kv = %q", v)
	}
}
