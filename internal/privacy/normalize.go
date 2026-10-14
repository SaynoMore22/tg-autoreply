package privacy

import (
	"strings"
	"unicode"
)

// Латинские буквы, которые выглядят как кириллические («прoмпт» с латинской o).
var homoglyphs = strings.NewReplacer(
	"a", "а", "e", "е", "o", "о", "p", "р", "c", "с", "x", "х", "y", "у",
	"k", "к", "m", "м", "t", "т", "h", "н", "b", "в", "u", "и", "n", "п",
	"3", "з", "0", "о", "6", "б",
)

// Canon — каноническая форма текста для поиска атак и запрещённых фрагментов:
// нижний регистр, ё→е, без невидимых символов, слова «в р а з р я д к у» склеены.
func Canon(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "ё", "е")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == 0x200B, r == 0x200C, r == 0x200D, r == 0x2060, r == 0xFEFF, r == 0x00AD:
			return -1
		case unicode.IsSpace(r):
			return ' '
		}
		return r
	}, s)
	return joinSpaced(strings.Join(strings.Fields(s), " "))
}

// joinSpaced склеивает 3+ одиночных символа подряд, разделённых пробелами, точками,
// дефисами, подчёркиваниями или звёздочками: «п р о м п т», «п.р.о.м.п.т» → «промпт».
func joinSpaced(s string) string {
	isSep := func(r rune) bool { return r == ' ' || r == '.' || r == '-' || r == '_' || r == '*' }
	rs := []rune(s)
	var out strings.Builder
	i := 0
	for i < len(rs) {
		// Пытаемся собрать цепочку «буква-разделитель-буква-...», где каждая буква одиночная.
		j := i
		var letters []rune
		for j < len(rs) && !isSep(rs[j]) && (j+1 == len(rs) || isSep(rs[j+1])) && (j == 0 || isSep(rs[j-1])) {
			letters = append(letters, rs[j])
			j++
			k := j
			for k < len(rs) && isSep(rs[k]) {
				k++
			}
			if k == j || k == len(rs) {
				break
			}
			j = k
		}
		if len(letters) >= 3 {
			out.WriteString(string(letters))
			for j < len(rs) && isSep(rs[j]) && rs[j] != ' ' {
				j++
			}
			i = j
			continue
		}
		out.WriteRune(rs[i])
		i++
	}
	return out.String()
}

// cyr заменяет латинские буквы-двойники на кириллицу в словах, где есть кириллица.
func cyr(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		hasCyr := false
		for _, r := range w {
			if unicode.Is(unicode.Cyrillic, r) {
				hasCyr = true
				break
			}
		}
		if hasCyr {
			words[i] = homoglyphs.Replace(w)
		}
	}
	return strings.Join(words, " ")
}

func digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}
