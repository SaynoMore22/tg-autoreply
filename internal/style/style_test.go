package style

import (
	"strings"
	"testing"

	"github.com/SaynoMore22/tg-autoreply/internal/tgx"
)

func TestSplitGroupsOwnerBursts(t *testing.T) {
	lines := []tgx.Line{
		{ID: 1, Sender: "Петя", Text: "го в кино"},
		{ID: 2, Out: true, Text: "го"},
		{ID: 3, Out: true, Text: "во сколько"},
		{ID: 4, Sender: "Петя", Text: "в 7"},
		{ID: 5, Out: true, Text: "[стикер 👍]"},
	}
	ex, own, bursts := split("u1", "Петя", lines)
	if len(ex) != 2 {
		t.Fatalf("exchanges = %d, want 2", len(ex))
	}
	if len(ex[0].Reply) != 2 || ex[0].Context[0] != "Петя: го в кино" {
		t.Fatalf("first exchange = %+v", ex[0])
	}
	if len(own) != 2 {
		t.Fatalf("own = %v, want 2 text messages without stickers", own)
	}
	if len(bursts) != 2 || bursts[0] != 2 || bursts[1] != 1 {
		t.Fatalf("bursts = %v", bursts)
	}
}

func TestComputeStats(t *testing.T) {
	s := computeStats([]string{"ахах ну да", "Ок.", "го?)"}, []int{1, 2})
	if s.Messages != 3 || s.AvgBurst != 1.5 {
		t.Fatalf("stats = %+v", s)
	}
	if s.Laughs["ахах"] != 1 {
		t.Fatalf("laughs = %v", s.Laughs)
	}
	if s.LowercaseStartPct < 66 || s.LowercaseStartPct > 67 {
		t.Fatalf("lowercase = %v", s.LowercaseStartPct)
	}
}

func TestPerChatStatsAreSeparate(t *testing.T) {
	friend := FromHistory("u1", "Друг", []tgx.Line{
		{ID: 1, Sender: "Друг", Text: "го"},
		{ID: 2, Out: true, Text: "ахах го бро"},
		{ID: 3, Out: true, Text: "ща"},
	})
	boss := FromHistory("u2", "Начальник", []tgx.Line{
		{ID: 1, Sender: "Начальник", Text: "Алексей, отчёт готов?"},
		{ID: 2, Out: true, Text: "Добрый день. Да, отправлю через час."},
	})
	if friend.Stats.LowercaseStartPct != 100 || boss.Stats.LowercaseStartPct != 0 {
		t.Fatalf("статистика смешалась: friend=%v boss=%v", friend.Stats.LowercaseStartPct, boss.Stats.LowercaseStartPct)
	}
	if friend.Stats.AvgBurst != 2 || boss.Stats.EndsWithDotPct != 100 {
		t.Fatalf("friend=%+v boss=%+v", friend.Stats, boss.Stats)
	}
	if friend.Exchanges[0].ChatKey != "u1" || boss.Exchanges[0].ChatKey != "u2" {
		t.Fatal("обмены не привязаны к своему чату")
	}
}

func TestAnonymousFormatHidesNames(t *testing.T) {
	e := Exchange{Chat: "Маша", Context: []string{"Маша: привет", "Я: ку"}, Reply: []string{"как ты"}}
	s := FormatExchange(e, true)
	if strings.Contains(s, "Маша") || !strings.Contains(s, "Собеседник: привет") || !strings.Contains(s, "Я: ку") {
		t.Fatalf("anon:\n%s", s)
	}
	if !strings.Contains(FormatExchange(e, false), "Маша") {
		t.Fatal("для своего чата имя должно остаться")
	}
}

func TestRedactedExchanges(t *testing.T) {
	ex := Redacted([]Exchange{{Context: []string{"Петя: мой номер +7 999 123 45 67"}, Reply: []string{"пиши на a@b.ru"}}})
	if strings.Contains(ex[0].Context[0], "999") || strings.Contains(ex[0].Reply[0], "a@b.ru") {
		t.Fatalf("не вычищено: %+v", ex[0])
	}
}
