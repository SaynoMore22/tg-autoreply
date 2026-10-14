// Мутационное тестирование: по очереди вносит в код заведомые поломки и проверяет,
// что тесты их ловят. Выжившая мутация = слабое место в тестах.
//
//	go run ./tools/mutate
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type mutation struct {
	file, pkg, desc, old, new string
}

var mutations = []mutation{
	// склейка серий и флуд
	{"internal/bot/bot.go", "./internal/bot", "порог флуда < вместо <=", "len(st.incoming) <= b.cfg.FloodMessages", "len(st.incoming) < b.cfg.FloodMessages"},
	{"internal/bot/bot.go", "./internal/bot", "флуд не включается", "st.floodUntil = now.Add(time.Duration(b.cfg.FloodCooldownMinutes) * time.Minute)", "st.floodUntil = now"},
	{"internal/bot/bot.go", "./internal/bot", "не растёт счётчик входящих", "\tst.gen++\n", "\n"},
	{"internal/bot/bot.go", "./internal/bot", "устаревший ответ не отбрасывается", "stale := st.gen != gen", "stale := false"},
	{"internal/bot/bot.go", "./internal/bot", "нет дедлайна серии", "if fire.After(st.deadline) {", "if false {"},
	{"internal/bot/bot.go", "./internal/bot", "«печатает» игнорируется", "if !ok || st.timer == nil || st.burstStart.IsZero() {", "if !ok || st.timer == nil || true {"},
	{"internal/bot/bot.go", "./internal/bot", "нет паузы на серию", "b.schedule(st, b.dur(b.cfg.DebounceSeconds))", "b.schedule(st, 0)"},
	// защита
	{"internal/bot/bot.go", "./internal/bot", "Guard выключен", "if reason := b.guard.Check(chat.Key, m); reason != \"\" {", "if reason := b.guard.Check(chat.Key, m); false && reason != \"\" {"},
	{"internal/bot/bot.go", "./internal/bot", "детектор атак выключен", "if isNew && privacy.LooksLikeAttack(l.Text) {", "if false && isNew && privacy.LooksLikeAttack(l.Text) {"},
	{"internal/bot/bot.go", "./internal/bot", "атака проверяется только в последнем сообщении", "isNew := !l.Out && l.ID > lastOut", "isNew := l.ID == lastIn"},
	{"internal/bot/bot.go", "./internal/bot", "в -dry отправка идёт", "\tif b.dryRun {\n\t\treturn\n\t}\n\n\tif b.cfg.MarkRead", "\n\tif b.cfg.MarkRead"},
	// лимиты и фильтры
	{"internal/bot/bot.go", "./internal/bot", "не уступает владельцу", "now.Sub(st.lastOwner) < pause", "now.Sub(st.lastOwner) < pause && false"},
	{"internal/bot/bot.go", "./internal/bot", "эхо бота = владелец", "_, echo := st.echoes[m.Message]", "echo := false"},
	{"internal/bot/bot.go", "./internal/bot", "лимит в чате > вместо >=", "len(st.sent) >= b.cfg.PerChatMaxPerHour", "len(st.sent) > b.cfg.PerChatMaxPerHour"},
	{"internal/bot/bot.go", "./internal/bot", "лимит модели > вместо >=", "len(st.modelCalls) >= b.cfg.PerChatMaxModelPerHour", "len(st.modelCalls) > b.cfg.PerChatMaxModelPerHour"},
	{"internal/bot/bot.go", "./internal/bot", "лимит ходов выключен", "st.botTurns >= b.cfg.MaxBotTurnsWithoutOwner", "st.botTurns < 0"},
	{"internal/bot/bot.go", "./internal/bot", "уведомление о лимите каждый раз", "\t\tif !noted {", "\t\tif !noted || true {"},
	{"internal/bot/bot.go", "./internal/bot", "нет ограничения числа частей", "if len(parts) == b.cfg.MaxParts {", "if false {"},
	{"internal/bot/bot.go", "./internal/bot", "в группе отвечает без отметки", "if !m.Mentioned || !b.cfg.ReplyInGroupsOnMention {", "if !b.cfg.ReplyInGroupsOnMention {"},
	{"internal/bot/bot.go", "./internal/bot", "чёрный список не работает", "if config.Matches(b.cfg.Blacklist, sender.ID, sender.Username) {", "if false {"},
	{"internal/bot/bot.go", "./internal/bot", "отвечает ботам", "sender.Bot || ", ""},
	// промпт
	{"internal/bot/prompt.go", "./internal/bot", "общие примеры не обезличены", "sb.WriteString(style.FormatExchange(e, true))", "sb.WriteString(style.FormatExchange(e, false))"},
	{"internal/bot/prompt.go", "./internal/bot", "persona_contacts всем", "\tcase contact:\n", "\tcase contact || true:\n"},
	{"internal/bot/prompt.go", "./internal/bot", "собеседник может закрыть блок переписки", "text := strings.ReplaceAll(l.Text, \"</переписка>\", \"\")", "text := l.Text"},
	{"internal/bot/prompt.go", "./internal/bot", "профиль чата не попадает в промпт", "\t\t\tsb.WriteString(p + \"\\n\")\n\t\t}\n\t}\n\tif len(sameChat)", "\t\t\t_ = p\n\t\t}\n\t}\n\tif len(sameChat)"},
	// privacy
	{"internal/privacy/privacy.go", "./internal/privacy", "без нормализации (латиница, вразрядку)", "variants := []string{s, c, cyr(c)}", "variants := []string{s, s, s + c[:0]}"},
	{"internal/privacy/privacy.go", "./internal/privacy", "номера не сравниваются по цифрам", "len(dd) >= 5 && strings.Contains(nd, dd)", "len(dd) >= 5 && strings.Contains(nd, dd) && false"},
	{"internal/privacy/privacy.go", "./internal/privacy", "свой чат тоже блокируется", "\t\tif key == chatKey {\n\t\t\tcontinue", "\t\tif key == chatKey && false {\n\t\t\tcontinue"},
	{"internal/privacy/privacy.go", "./internal/privacy", "метка промпта не проверяется", "if g.Canary != \"\" && strings.Contains(n, strings.ToLower(g.Canary)) {", "if false {"},
	{"internal/privacy/privacy.go", "./internal/privacy", "Redact ничего не делает", "s = p.re.ReplaceAllString(s, \"[\"+p.name+\"]\")", "_ = p"},
	{"internal/privacy/privacy.go", "./internal/privacy", "ссылки разрешены всегда", "if kind != \"ссылка\" || !g.AllowLinks {", "if kind != \"ссылка\" {"},
	{"internal/privacy/normalize.go", "./internal/privacy", "ё не нормализуется", "s = strings.ReplaceAll(s, \"ё\", \"е\")", ""},
	{"internal/privacy/normalize.go", "./internal/privacy", "склейка от 2 букв", "if len(letters) >= 3 {", "if len(letters) >= 2 {"},
	// стиль
	{"internal/style/style.go", "./internal/style", "стикеры считаются текстом", "!strings.HasPrefix(lines[i].Text, \"[\")", "true"},
	{"internal/style/style.go", "./internal/style", "неверный подсчёт регистра", "unicode.IsLetter(first) && unicode.IsLower(first)", "unicode.IsLetter(first) && unicode.IsUpper(first)"},
	{"internal/style/style.go", "./internal/style", "анонимизация не скрывает имена", "if anon && !strings.HasPrefix(l, \"Я: \") {", "if false {"},
	// архив
	{"internal/bot/bot.go", "./internal/bot", "архивные чаты не блокируются", "if b.isArchived(chat.Key) {\n\t\treturn // архив", "if false && b.isArchived(chat.Key) {\n\t\treturn // архив"},
	{"internal/bot/bot.go", "./internal/bot", "архив не проверяется перед ответом", "if b.isArchived(chat.Key) {\n\t\treturn // чат успели", "if false && b.isArchived(chat.Key) {\n\t\treturn // чат успели"},
	{"internal/bot/bot.go", "./internal/bot", "перенос в архив игнорируется", "fp.FolderID == tgx.ArchiveFolder", "fp.FolderID == 99"},
	// рекап
	{"internal/recap/recap.go", "./internal/recap", "рекап несколько раз в день", "if last, _ := r.Store.Get(keyDate); last == today {", "if false {"},
	{"internal/recap/recap.go", "./internal/recap", "cutoff не сдвигается", "return r.Store.Set(keyCutoff, strconv.FormatInt(to.Unix(), 10))", "return nil"},
	{"internal/recap/recap.go", "./internal/recap", "пустой день молчит", "text = \"🗒 \" + title + \"\\nПереписок не было.\"", "return nil"},
}

func main() {
	only := map[int]bool{}
	for _, s := range strings.Split(os.Getenv("MUTATE_ONLY"), ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			only[n] = true
		}
	}
	var survived []string
	killed, skipped := 0, 0
	for i, m := range mutations {
		if len(only) > 0 && !only[i+1] {
			continue
		}
		orig, err := os.ReadFile(m.file)
		if err != nil {
			panic(err)
		}
		src := string(orig)
		if strings.Count(src, m.old) != 1 {
			fmt.Printf("[%2d] ПРОПУЩЕНА (фрагмент найден %d раз): %s\n", i+1, strings.Count(src, m.old), m.desc)
			skipped++
			continue
		}
		if err := os.WriteFile(m.file, []byte(strings.Replace(src, m.old, m.new, 1)), 0o644); err != nil {
			panic(err)
		}
		start := time.Now()
		out, err := exec.Command("go", "test", "-count=1", "-timeout=240s", m.pkg).CombinedOutput()
		if werr := os.WriteFile(m.file, orig, 0o644); werr != nil {
			panic(werr) // файл обязан вернуться в исходное состояние
		}
		switch {
		case err == nil:
			fmt.Printf("[%2d] ВЫЖИЛА  (%s): %s\n", i+1, time.Since(start).Round(time.Second), m.desc)
			survived = append(survived, m.desc)
		case strings.Contains(string(out), "build failed") || strings.Contains(string(out), "[setup failed]"):
			fmt.Printf("[%2d] НЕ СОБРАЛАСЬ: %s\n", i+1, m.desc)
			skipped++
		default:
			fmt.Printf("[%2d] убита   (%s): %s\n", i+1, time.Since(start).Round(time.Second), m.desc)
			killed++
		}
	}
	fmt.Printf("\nИТОГО: убито %d, выжило %d, пропущено %d из %d\n", killed, len(survived), skipped, len(mutations))
	for _, s := range survived {
		fmt.Println("  выжила:", s)
	}
	if len(survived) > 0 {
		os.Exit(1)
	}
}
