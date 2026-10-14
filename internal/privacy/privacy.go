// Package privacy — защита от утечек: вычистка личных данных из корпуса,
// проверка исходящих сообщений и детектор попыток взлома промпта.
// Проверки работают в коде и не зависят от того, послушается ли модель.
package privacy

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

type pattern struct {
	re   *regexp.Regexp
	name string
}

// Порядок важен: сначала более специфичные.
var sensitive = []pattern{
	{regexp.MustCompile(`(?i)\b(?:https?://|www\.|t\.me/|tg://)\S+`), "ссылка"},
	{regexp.MustCompile(`[\p{L}\d._%+-]+@[\p{L}\d-]+\.[\p{L}\d.-]+`), "почта"},
	{regexp.MustCompile(`\b(?:0x[a-fA-F0-9]{40}|bc1[a-z0-9]{25,60}|[13][a-km-zA-HJ-NP-Z1-9]{25,34}|T[A-Za-z1-9]{33})\b`), "кошелёк"},
	{regexp.MustCompile(`\b[A-Z]{2}\d{2}[A-Z0-9]{11,30}\b`), "счёт"},
	{regexp.MustCompile(`(?:\d[ -]?){13,19}`), "номер карты"},
	{regexp.MustCompile(`\+?\d[\d\s()\-]{8,}\d`), "телефон"},
	{regexp.MustCompile(`\d{5,}`), "код или номер"},
}

// Redact заменяет в тексте телефоны, почты, карты, ссылки, кошельки и длинные числа на метки.
func Redact(s string) string {
	for _, p := range sensitive {
		s = p.re.ReplaceAllString(s, "["+p.name+"]")
	}
	return s
}

// Sensitive возвращает название первого найденного вида личных данных или "".
func Sensitive(s string) string {
	for _, p := range sensitive {
		if p.re.MatchString(s) {
			return p.name
		}
	}
	return ""
}

var injection = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(игнорир|забудь|отмени|не слушай)\p{L}*\s+(\p{L}+\s+){0,3}(инструкц|правил|указани|промпт|настрой)`),
	regexp.MustCompile(`(?i)(систем\p{L}*\s+промпт|system\s*prompt|prompt|промпт)`),
	regexp.MustCompile(`(?i)ignore\s+(all|any|the|previous|above|prior)`),
	regexp.MustCompile(`(?i)(developer|dev|god|admin|debug)\s*mode|режим\s+(разработчика|отладки|администратора|бога)|jailbreak|dan\s+mode`),
	regexp.MustCompile(`(?i)(покажи|выведи|напиши|скинь|перешли|повтори)\s+(\p{L}+\s+){0,3}(свои|твои|все|системн\p{L}*)\s+(инструкц|правил|настрой|сообщени|переписк|чат)`),
	// \b в RE2 не понимает кириллицу, поэтому границы слов — через \P{L}.
	regexp.MustCompile(`(?i)(?:^|\P{L})(ты|вы|это)[\s,]+(\p{L}+[\s,]+){0,2}(бот|робот|ии|нейросет\p{L}*|автоответчик\p{L}*|chatgpt|gpt|claude|искусственн\p{L}*\s+интеллект)(?:\P{L}|$)`),
	regexp.MustCompile(`(?i)\b(you\s+are|are\s+you)\s+(an?\s+)?(ai|bot|language\s+model|llm|claude|gpt)\b`),
	// «отвечаешь как робот», «ты вообще живой человек?», «это реально ты пишешь?»
	regexp.MustCompile(`(?i)(отвеча\p{L}*|пиш\p{L}*|звуч\p{L}*)\s+(\p{L}+\s+){0,2}как\s+(робот|бот|нейросет\p{L}*|ии|автоответчик)|живой\s+человек|(ты|это)\s+(реально|правда|точно)\s+(ты|сам)\s*(пишешь|отвечаешь)?\s*\?`),
	regexp.MustCompile(`(?i)(от\s+имени|как)\s+(администратор|разработчик|anthropic|телеграм|telegram)`),
	regexp.MustCompile(`(?i)(с\s+кем|кому)\s+(\p{L}+\s+){0,2}(переписыва|общаешься|пишешь)|что\s+тебе\s+(пишут|писал\p{L}*)|перешли\s+переписк`),
	regexp.MustCompile(`(?i)(?:^|\P{L})(паспорт|снилс|cvv|cvc|пин-?код|парол\p{L}*|seed|сид-?фраз)|(?:^|\P{L})инн(?:\P{L}|$)|номер\s+(твоей\s+)?карт|код\s+из\s+(смс|sms|телеграм|telegram)`),
}

// LooksLikeAttack — похоже ли входящее на попытку вытащить промпт, чужие переписки
// или выяснить, что отвечает бот. Такие сообщения бот не обрабатывает, а зовёт владельца.
func LooksLikeAttack(s string) bool {
	c := Canon(s)
	variants := []string{s, c, cyr(c)}
	for _, re := range injection {
		if re.MatchString(variants[0]) || re.MatchString(variants[1]) || re.MatchString(variants[2]) {
			return true
		}
	}
	return false
}

// Guard проверяет исходящие сообщения перед отправкой.
type Guard struct {
	AllowLinks bool
	Canary     string              // метка в системном промпте: если утекла — блок
	denylist   []string            // то, что нельзя писать никогда (из private.txt)
	snippets   []string            // куски системного промпта
	foreign    map[string][]string // чужие реплики из других чатов: chatKey -> фрагменты
}

func NewGuard(allowLinks bool) *Guard {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return &Guard{AllowLinks: allowLinks, Canary: "ЗК-" + hex.EncodeToString(b[:]), foreign: map[string][]string{}}
}

func norm(s string) string { return Canon(s) }

// LoadDenylist читает файл: одна строка — один запрещённый фрагмент, # — комментарий.
func (g *Guard) LoadDenylist(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		g.denylist = append(g.denylist, norm(line))
	}
	return sc.Err()
}

// AddPromptText запоминает длинные строки системного промпта, чтобы поймать их пересказ дословно.
func (g *Guard) AddPromptText(text string) {
	for _, line := range strings.Split(text, "\n") {
		if n := norm(line); utf8.RuneCountInString(n) >= 40 {
			g.snippets = append(g.snippets, n)
		}
	}
}

// AddForeign запоминает реплику собеседника из чата chatKey.
func (g *Guard) AddForeign(chatKey, text string) {
	if n := norm(text); utf8.RuneCountInString(n) >= 20 {
		g.foreign[chatKey] = append(g.foreign[chatKey], n)
	}
}

// Check возвращает причину блокировки или "".
func (g *Guard) Check(chatKey, text string) string {
	n := norm(text)
	if g.Canary != "" && strings.Contains(n, strings.ToLower(g.Canary)) {
		return "попытка вывести системный промпт"
	}
	nd := digits(text)
	for _, d := range g.denylist {
		if strings.Contains(n, d) || strings.Contains(cyr(n), cyr(d)) {
			return "фрагмент из private.txt"
		}
		// Номера сравниваем только по цифрам: «9 5 2-714» == «952714».
		if dd := digits(d); len(dd) >= 5 && strings.Contains(nd, dd) {
			return "фрагмент из private.txt"
		}
	}
	if kind := Sensitive(text); kind != "" {
		if kind != "ссылка" || !g.AllowLinks {
			return "в ответе " + kind
		}
	}
	for _, s := range g.snippets {
		if strings.Contains(n, s) {
			return "пересказ системного промпта"
		}
	}
	for key, list := range g.foreign {
		if key == chatKey {
			continue
		}
		for _, s := range list {
			if strings.Contains(n, s) {
				return "цитата из чужой переписки"
			}
		}
	}
	return ""
}
