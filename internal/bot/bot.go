// Package bot — логика автоответов: фильтры, склейка серий, антиспам,
// решение модели, проверки приватности и отправка.
package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/tg"

	"github.com/SaynoMore22/tg-autoreply/internal/config"
	"github.com/SaynoMore22/tg-autoreply/internal/llm"
	"github.com/SaynoMore22/tg-autoreply/internal/privacy"
	"github.com/SaynoMore22/tg-autoreply/internal/store"
	"github.com/SaynoMore22/tg-autoreply/internal/style"
	"github.com/SaynoMore22/tg-autoreply/internal/tgx"
)

// Persona — что боту можно рассказывать о владельце.
type Persona struct {
	Public   string // persona.md — всем
	Contacts string // persona_contacts.md — только тем, кто в контактах, и только в личке
}

type Bot struct {
	ctx    context.Context // живёт всё время работы бота
	cfg    config.Config
	tg     tgx.Messenger
	model  llm.Model
	store  *store.Store
	selfID int64
	dryRun bool

	system  string
	general string // общий профиль стиля
	persona Persona
	guard   *privacy.Guard

	// OnChatStyle вызывается, когда для нового чата построен профиль (чтобы сохранить на диск).
	OnChatStyle func(*style.ChatStyle)

	mu         sync.Mutex
	archived   map[string]bool // чаты из архива: бот их не читает и не отвечает
	styles     map[string]*style.ChatStyle
	building   map[string]bool
	chats      map[string]*chatState
	users      map[int64]*tg.User
	chatsTG    map[int64]*tg.Chat
	channels   map[int64]*tg.Channel
	global     []time.Time
	modelCalls int // всего вызовов модели (для тестов и логов)
}

type chatState struct {
	chat      tgx.Chat
	timer     *time.Timer
	running   bool
	again     bool
	lastIn    int // id последнего входящего
	mentionID int // id сообщения с отметкой (группы)

	gen        int       // растёт с каждым входящим: по нему видно, что пришло новое, пока бот думал
	burstStart time.Time // начало текущей серии входящих
	deadline   time.Time // дольше этого серию не ждём

	incoming   []time.Time
	floodUntil time.Time
	modelCalls []time.Time

	botTurns      int
	lastOwner     time.Time
	sent          []time.Time
	echoes        map[string]time.Time
	limitNoted    bool
	strangerNoted time.Time
}

type decision struct {
	Action    string   `json:"action"`
	Messages  []string `json:"messages"`
	Reaction  string   `json:"reaction"`
	OwnerNote string   `json:"owner_note"`
}

func New(ctx context.Context, cfg config.Config, m tgx.Messenger, model llm.Model, st *store.Store, selfID int64, persona Persona, profile string, corpus *style.Corpus, guard *privacy.Guard, dryRun bool) *Bot {
	b := &Bot{
		ctx:      ctx,
		cfg:      cfg,
		tg:       m,
		model:    model,
		store:    st,
		selfID:   selfID,
		dryRun:   dryRun,
		general:  privacy.Redact(profile),
		persona:  persona,
		guard:    guard,
		archived: map[string]bool{},
		styles:   map[string]*style.ChatStyle{},
		building: map[string]bool{},
		chats:    map[string]*chatState{},
		users:    map[int64]*tg.User{},
		chatsTG:  map[int64]*tg.Chat{},
		channels: map[int64]*tg.Channel{},
	}
	var examples []style.Exchange
	if corpus != nil {
		// Корпус вычищается от телефонов, почт, карт и ссылок до попадания в промпт.
		examples = style.Sample(style.Redacted(corpus.Exchanges), cfg.StyleExamplesInPrompt)
		for key, cs := range corpus.Chats {
			b.addStyle(key, cs)
		}
	}
	system, guarded := buildSystem(cfg, persona.Public, b.general, guard.Canary, examples)
	b.system = system
	guard.AddPromptText(guarded)
	return b
}

// addStyle регистрирует профиль чата и запоминает чужие реплики из него для Guard.
func (b *Bot) addStyle(key string, cs *style.ChatStyle) {
	c := *cs
	c.Exchanges = style.Redacted(cs.Exchanges)
	c.Profile = privacy.Redact(cs.Profile)
	for _, e := range cs.Exchanges {
		for _, line := range e.Context {
			if i := strings.Index(line, ": "); i >= 0 && !strings.HasPrefix(line, "Я: ") {
				b.guard.AddForeign(key, line[i+2:])
			}
		}
	}
	b.mu.Lock()
	b.styles[key] = &c
	b.mu.Unlock()
}

// ModelCalls — сколько раз вызывалась модель.
func (b *Bot) ModelCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.modelCalls
}

// Remember кеширует сущности (нужны access_hash для ответа).
func (b *Bot) Remember(e tg.Entities) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, u := range e.Users {
		if _, ok := b.users[id]; ok && u.Min {
			continue
		}
		b.users[id] = u
	}
	for id, c := range e.Chats {
		b.chatsTG[id] = c
	}
	for id, c := range e.Channels {
		if _, ok := b.channels[id]; ok && c.Min {
			continue
		}
		b.channels[id] = c
	}
}

// Preload кеширует пользователей и чаты из последних limit диалогов.
func (b *Bot) Preload(ctx context.Context, limit int) {
	e, err := b.tg.Dialogs(ctx, limit)
	if err != nil {
		log.Printf("диалоги: %v", err)
		return
	}
	b.Remember(e)
}

func (b *Bot) known(peer tg.PeerClass) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch p := peer.(type) {
	case *tg.PeerUser:
		_, ok := b.users[p.UserID]
		return ok
	case *tg.PeerChat:
		_, ok := b.chatsTG[p.ChatID]
		return ok
	case *tg.PeerChannel:
		_, ok := b.channels[p.ChannelID]
		return ok
	}
	return true
}

func (b *Bot) resolve(peer tg.PeerClass) (tgx.Chat, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return tgx.ResolveChat(peer, b.users, b.chatsTG, b.channels)
}

func (b *Bot) sender(m *tg.Message) *tg.User {
	b.mu.Lock()
	defer b.mu.Unlock()
	if from, ok := m.FromID.(*tg.PeerUser); ok {
		return b.users[from.UserID]
	}
	if p, ok := m.PeerID.(*tg.PeerUser); ok {
		return b.users[p.UserID]
	}
	return nil
}

func (b *Bot) state(chat tgx.Chat) *chatState {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.chats[chat.Key]
	if !ok {
		st = &chatState{echoes: map[string]time.Time{}}
		b.chats[chat.Key] = st
	}
	st.chat = chat
	return st
}

// OnMessage вызывается на каждое новое сообщение (личка, группы, каналы).
func (b *Bot) OnMessage(ctx context.Context, e tg.Entities, mc tg.MessageClass) {
	m, ok := mc.(*tg.Message)
	if !ok {
		return
	}
	b.Remember(e)

	chat, ok := b.resolve(m.PeerID)
	if !ok {
		if b.known(m.PeerID) {
			return // канал-вещалка
		}
		// Короткие апдейты приходят без сущностей: подтягиваем свежие диалоги.
		b.Preload(ctx, 20)
		if chat, ok = b.resolve(m.PeerID); !ok {
			return
		}
	}
	if chat.User != nil && (chat.User.Self || chat.User.ID == b.selfID) {
		return // «Избранное»
	}
	if b.isArchived(chat.Key) {
		return // архив: ни чтения, ни журнала, ни ответов
	}
	text := tgx.Describe(m)
	now := time.Now()
	st := b.state(chat)

	if m.Out {
		b.mu.Lock()
		_, echo := st.echoes[m.Message]
		if echo {
			delete(st.echoes, m.Message)
		} else {
			st.lastOwner = now
			st.botTurns = 0
			st.limitNoted = false
		}
		b.mu.Unlock()
		if !echo && !chat.IsGroup {
			b.log(chat, "Я", true, false, text)
		}
		return
	}

	sender := b.sender(m)
	if sender == nil || sender.Bot || sender.ID == 777000 || sender.Support {
		return
	}
	if config.Matches(b.cfg.Blacklist, sender.ID, sender.Username) {
		return
	}
	if len(b.cfg.Whitelist) > 0 && !config.Matches(b.cfg.Whitelist, sender.ID, sender.Username) {
		return
	}
	if chat.IsGroup {
		if !m.Mentioned || !b.cfg.ReplyInGroupsOnMention {
			return
		}
		b.log(chat, tgx.UserName(sender), false, false, text)
	} else {
		b.log(chat, tgx.UserName(sender), false, false, text)
		if !b.cfg.ReplyPrivate || (b.cfg.OnlyContacts && !sender.Contact) {
			return
		}
	}

	b.mu.Lock()
	st.lastIn = m.ID
	st.gen++
	if chat.IsGroup {
		st.mentionID = m.ID
	}
	if flooded := b.checkFlood(st, now); flooded {
		b.mu.Unlock()
		return
	}
	b.schedule(st, b.dur(b.cfg.DebounceSeconds))
	b.mu.Unlock()
}

// checkFlood (под b.mu): считает входящие и включает режим тишины при флуде.
func (b *Bot) checkFlood(st *chatState, now time.Time) bool {
	if now.Before(st.floodUntil) {
		return true
	}
	window := now.Add(-time.Duration(b.cfg.FloodWindowSeconds) * time.Second)
	st.incoming = append(trim(st.incoming, window), now)
	if b.cfg.FloodMessages <= 0 || len(st.incoming) <= b.cfg.FloodMessages {
		return false
	}
	st.floodUntil = now.Add(time.Duration(b.cfg.FloodCooldownMinutes) * time.Minute)
	st.incoming = nil
	if st.timer != nil {
		st.timer.Stop()
	}
	st.burstStart = time.Time{}
	chat := st.chat
	go func() {
		log.Printf("[%s] флуд: больше %d сообщений за %d с, молчу %d мин", chat.Title, b.cfg.FloodMessages, b.cfg.FloodWindowSeconds, b.cfg.FloodCooldownMinutes)
		b.event(chat, "flood", fmt.Sprintf("больше %d сообщений за %d с", b.cfg.FloodMessages, b.cfg.FloodWindowSeconds))
		b.notify(b.ctx, fmt.Sprintf("🛡 В «%s» флуд — больше %d сообщений за %d с. Бот молчит в этом чате %d мин.", chat.Title, b.cfg.FloodMessages, b.cfg.FloodWindowSeconds, b.cfg.FloodCooldownMinutes))
	}()
	return true
}

// OnTyping — собеседник печатает: если серия ещё не обработана, подождём продолжения.
func (b *Bot) OnTyping(userID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.chats[fmt.Sprintf("u%d", userID)]
	if !ok || st.timer == nil || st.burstStart.IsZero() {
		return
	}
	b.schedule(st, b.dur(b.cfg.TypingExtendSeconds))
}

func (b *Bot) dur(sec float64) time.Duration { return time.Duration(sec * float64(time.Second)) }

// schedule (под b.mu): отложить обработку на wait, но не позже дедлайна серии.
func (b *Bot) schedule(st *chatState, wait time.Duration) {
	now := time.Now()
	if st.burstStart.IsZero() {
		st.burstStart = now
		st.deadline = now.Add(b.dur(b.cfg.MaxWaitSeconds))
	}
	fire := now.Add(wait)
	if fire.After(st.deadline) {
		fire = st.deadline
	}
	if st.timer != nil {
		st.timer.Stop()
	}
	st.timer = time.AfterFunc(time.Until(fire), func() { b.run(b.ctx, st) })
}

func (b *Bot) log(chat tgx.Chat, sender string, out, byBot bool, text string) {
	if text == "" || b.store == nil {
		return
	}
	if err := b.store.AddMessage(store.Message{
		ChatKey: chat.Key, ChatTitle: chat.Title, Sender: sender,
		Out: out, ByBot: byBot, Text: text, At: time.Now(),
	}); err != nil {
		log.Printf("store: %v", err)
	}
}

func (b *Bot) event(chat tgx.Chat, kind, note string) {
	if b.store == nil {
		return
	}
	_ = b.store.AddEvent(store.Event{ChatKey: chat.Key, ChatTitle: chat.Title, Kind: kind, Note: note, At: time.Now()})
}

// run — не больше одной обработки на чат одновременно.
func (b *Bot) run(ctx context.Context, st *chatState) {
	b.mu.Lock()
	if st.running {
		st.again = true
		b.mu.Unlock()
		return
	}
	st.running = true
	b.mu.Unlock()

	for {
		b.process(ctx, st)
		b.mu.Lock()
		if !st.again {
			st.running = false
			b.mu.Unlock()
			return
		}
		st.again = false
		b.mu.Unlock()
	}
}

// allow проверяет антиспам-лимиты. Возвращает причину отказа или "".
func (b *Bot) allow(st *chatState) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if pause := time.Duration(b.cfg.OwnerActivePauseMinutes) * time.Minute; now.Sub(st.lastOwner) < pause {
		return "owner_active"
	}
	if now.Before(st.floodUntil) {
		return "flood"
	}
	if b.cfg.MaxBotTurnsWithoutOwner > 0 && st.botTurns >= b.cfg.MaxBotTurnsWithoutOwner {
		return "лимит ответов подряд без тебя"
	}
	hourAgo := now.Add(-time.Hour)
	st.sent = trim(st.sent, hourAgo)
	st.modelCalls = trim(st.modelCalls, hourAgo)
	b.global = trim(b.global, hourAgo)
	if len(st.sent) >= b.cfg.PerChatMaxPerHour {
		return "часовой лимит сообщений в чате"
	}
	if len(b.global) >= b.cfg.GlobalMaxPerHour {
		return "общий часовой лимит сообщений"
	}
	if b.cfg.PerChatMaxModelPerHour > 0 && len(st.modelCalls) >= b.cfg.PerChatMaxModelPerHour {
		return "часовой лимит обращений к модели в чате"
	}
	return ""
}

func trim(ts []time.Time, after time.Time) []time.Time {
	i := 0
	for i < len(ts) && ts[i].Before(after) {
		i++
	}
	return ts[i:]
}

// chatStyle возвращает профиль общения с этим человеком; для новых чатов
// разбирает историю сразу, а подробный профиль строит в фоне.
func (b *Bot) chatStyle(ctx context.Context, chat tgx.Chat) *style.ChatStyle {
	b.mu.Lock()
	cs, ok := b.styles[chat.Key]
	busy := b.building[chat.Key]
	b.mu.Unlock()
	if ok || busy || chat.IsGroup {
		return cs
	}
	b.mu.Lock()
	b.building[chat.Key] = true
	b.mu.Unlock()

	var lines []tgx.Line
	offset := 0
	for len(lines) < b.cfg.StyleMessagesPerChat {
		batch, err := b.tg.History(ctx, chat, 100, offset)
		if err != nil || len(batch) == 0 {
			break
		}
		lines = append(batch, lines...)
		offset = batch[0].ID
		if len(batch) < 100 {
			break
		}
	}
	cs = style.FromHistory(chat.Key, chat.Title, lines)
	b.addStyle(chat.Key, cs)
	if cs.Stats.Messages >= style.MinOwnMessages && b.general != "" {
		go func() {
			p, err := style.BuildChatProfile(b.ctx, b.model, b.cfg.ChatProfileEffort, b.cfg.OwnerName, b.general, cs)
			if err != nil {
				log.Printf("[%s] профиль чата: %v", chat.Title, err)
				return
			}
			cs.Profile = p
			b.addStyle(chat.Key, cs)
			log.Printf("[%s] построен профиль общения с этим человеком", chat.Title)
			if b.OnChatStyle != nil {
				b.OnChatStyle(cs)
			}
		}()
	}
	b.mu.Lock()
	out := b.styles[chat.Key]
	b.mu.Unlock()
	return out
}

// examplesFor — свежие и случайные прошлые обмены именно с этим человеком.
func (b *Bot) examplesFor(cs *style.ChatStyle) []style.Exchange {
	n := b.cfg.SameChatExamples
	if cs == nil || n <= 0 || len(cs.Exchanges) == 0 {
		return nil
	}
	ex := cs.Exchanges
	if len(ex) <= n {
		return ex
	}
	recent := ex[len(ex)-n/2:]
	older := ex[:len(ex)-n/2]
	var picked []style.Exchange
	for _, i := range rand.Perm(len(older))[:n-len(recent)] {
		picked = append(picked, older[i])
	}
	return append(picked, recent...)
}

func (b *Bot) process(ctx context.Context, st *chatState) {
	b.mu.Lock()
	chat, lastIn, mentionID, gen := st.chat, st.lastIn, st.mentionID, st.gen
	st.burstStart = time.Time{} // следующая серия начнётся заново
	b.mu.Unlock()

	if b.isArchived(chat.Key) {
		return // чат успели убрать в архив, пока бот ждал
	}

	if reason := b.allow(st); reason != "" {
		if reason == "owner_active" || reason == "flood" {
			log.Printf("[%s] пропуск: %s", chat.Title, reason)
			return
		}
		b.mu.Lock()
		noted := st.limitNoted
		st.limitNoted = true
		b.mu.Unlock()
		log.Printf("[%s] пропуск: %s", chat.Title, reason)
		if !noted {
			b.event(chat, "limit", reason)
			b.notify(ctx, fmt.Sprintf("🤖 Бот замолчал в чате «%s»: %s. Ответь сам — счётчик сбросится.", chat.Title, reason))
		}
		return
	}

	lines, err := b.tg.History(ctx, chat, b.cfg.HistoryMessages, 0)
	if err != nil {
		log.Printf("[%s] история: %v", chat.Title, err)
		return
	}
	lastOut := 0
	for _, l := range lines {
		if l.Out {
			lastOut = l.ID
		}
	}
	if !chat.IsGroup && lastOut > lastIn {
		return // уже ответили (сам или бот)
	}

	// Попытки выведать промпт, чужие переписки, коды или спросить «ты бот?»
	// в модель не отправляются вообще: бот молчит и зовёт владельца.
	for _, l := range lines {
		isNew := !l.Out && l.ID > lastOut
		if chat.IsGroup {
			isNew = l.ID == mentionID
		}
		if isNew && privacy.LooksLikeAttack(l.Text) {
			log.Printf("[%s] подозрительное сообщение, бот молчит: %q", chat.Title, l.Text)
			if !b.dryRun {
				b.event(chat, "defer", "подозрительный запрос: "+l.Text)
				b.notify(ctx, fmt.Sprintf("🛡 В «%s» похоже на попытку выведать данные или проверить, бот ли отвечает. Бот промолчал, ответь сам:\n«%s»", chat.Title, l.Text))
			}
			return
		}
	}

	// Незнакомцам бот сам не отвечает (типичная схема: «курьер», «поддержка», «это Алексей?»),
	// а сообщает владельцу — не чаще раза в 6 часов на чат.
	if chat.User != nil && !chat.User.Contact && b.cfg.StrangerMode != "reply" {
		b.mu.Lock()
		noted := time.Since(st.strangerNoted) < 6*time.Hour
		if !noted {
			st.strangerNoted = time.Now()
		}
		b.mu.Unlock()
		log.Printf("[%s] незнакомец, бот не отвечает (stranger_mode=%s)", chat.Title, b.cfg.StrangerMode)
		if !noted && !b.dryRun {
			last := ""
			if len(lines) > 0 {
				last = lines[len(lines)-1].Text
			}
			b.event(chat, "defer", "пишет незнакомец")
			b.notify(ctx, fmt.Sprintf("👤 Тебе пишет незнакомец «%s» (нет в контактах). Бот не отвечает, ответь сам:\n«%s»", chat.Title, last))
		}
		return
	}

	cs := b.chatStyle(ctx, chat)

	b.mu.Lock()
	st.modelCalls = append(st.modelCalls, time.Now())
	b.modelCalls++
	b.mu.Unlock()

	started := time.Now()
	raw, err := b.model.Complete(ctx, llm.Request{
		System:    b.system,
		User:      buildUser(chat, b.persona.Contacts, cs, b.examplesFor(cs), lines, lastOut, mentionID),
		Effort:    b.cfg.ReplyEffort,
		MaxTokens: 4000,
		Schema:    decisionSchema(),
	})
	if err != nil {
		log.Printf("[%s] модель: %v", chat.Title, err)
		b.event(chat, "error", err.Error())
		return
	}
	var d decision
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		log.Printf("[%s] разбор ответа модели: %v: %s", chat.Title, err, raw)
		return
	}
	log.Printf("[%s] решение за %s: %s %q реакция=%q %s", chat.Title, time.Since(started).Round(100*time.Millisecond), d.Action, d.Messages, d.Reaction, d.OwnerNote)

	// Пока модель думала, собеседник дописал — ответ устарел, пересоберём с полным контекстом.
	b.mu.Lock()
	stale := st.gen != gen
	ownerJumpedIn := st.lastOwner.After(started)
	b.mu.Unlock()
	if stale {
		log.Printf("[%s] пришли новые сообщения, ответ пересобирается", chat.Title)
		return
	}
	if ownerJumpedIn {
		log.Printf("[%s] ты ответил сам, бот молчит", chat.Title)
		return
	}

	// Сломанный ответ: модель повторила сообщения собеседника («слушай / а ты / в субботу»).
	// Одно совпадение допустимо («го» в ответ на «го»), два и больше — мусор.
	if d.Action == "reply" && parroted(d.Messages, lines) >= 2 {
		log.Printf("[%s] модель повторила сообщения собеседника, ответ выброшен: %q", chat.Title, d.Messages)
		return
	}

	// Последний рубеж: проверка каждого сообщения кодом, независимо от модели.
	if d.Action == "reply" {
		for _, m := range d.Messages {
			if reason := b.guard.Check(chat.Key, m); reason != "" {
				log.Printf("[%s] ЗАБЛОКИРОВАНО (%s): %q", chat.Title, reason, m)
				if !b.dryRun {
					b.event(chat, "blocked", reason)
					b.notify(ctx, fmt.Sprintf("🛡 Бот хотел ответить в «%s», но проверка заблокировала ответ (%s). Ничего не отправлено, ответь сам.", chat.Title, reason))
				}
				return
			}
		}
		// Обещания и конкретное время за владельца — только он сам.
		// Проверяются после утечек, чтобы об утечке ты узнал в первую очередь.
		for _, m := range d.Messages {
			if b.cfg.BlockPromises && privacy.Commitment(m) {
				log.Printf("[%s] ЗАБЛОКИРОВАНО (обещание): %q", chat.Title, m)
				if !b.dryRun {
					b.event(chat, "defer", "бот хотел пообещать: "+m)
					b.notify(ctx, fmt.Sprintf("🤖 В «%s» бот хотел ответить «%s», но это обещание или конкретное время — ответь сам.", chat.Title, strings.Join(d.Messages, " / ")))
				}
				return
			}
		}
		// Реакция вместе с текстом — лишь иногда, как у живого человека.
		if d.Reaction != "" && rand.Float64() >= b.cfg.ReactionWithReplyChance {
			d.Reaction = ""
		}
	}

	if b.dryRun {
		return
	}

	if b.cfg.MarkRead && d.Action != "defer" {
		b.tg.MarkRead(ctx, chat, lastIn)
	}

	if b.cfg.ReactionsEnabled && d.Reaction != "" && (d.Action == "reply" || d.Action == "react") {
		target := lastIn
		if chat.IsGroup && mentionID != 0 {
			target = mentionID
		}
		if err := b.tg.React(ctx, chat, target, d.Reaction); err != nil {
			log.Printf("[%s] реакция: %v", chat.Title, err)
		}
	}

	switch d.Action {
	case "reply":
		b.send(ctx, st, chat, d.Messages, mentionID, gen)
	case "defer":
		note := strings.TrimSpace(d.OwnerNote)
		if note == "" {
			note = "нужен твой ответ"
		}
		b.event(chat, "defer", note)
		b.notify(ctx, fmt.Sprintf("🤖 Не стал отвечать в «%s»: %s", chat.Title, note))
	}
}

func (b *Bot) send(ctx context.Context, st *chatState, chat tgx.Chat, msgs []string, mentionID, gen int) {
	var parts []string
	for _, m := range msgs {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if utf8.RuneCountInString(m) > b.cfg.MaxCharsPerPart {
			m = string([]rune(m)[:b.cfg.MaxCharsPerPart])
		}
		parts = append(parts, m)
		if len(parts) == b.cfg.MaxParts {
			break
		}
	}
	for i, p := range parts {
		if b.cfg.BotMarker != "" {
			p = p + " " + b.cfg.BotMarker
		}
		b.tg.Typing(ctx, chat)
		typing := min(float64(utf8.RuneCountInString(p))/b.cfg.TypingCharsPerSecond, b.cfg.MaxTypingSeconds)
		if i > 0 {
			typing += b.cfg.PauseBetweenPartsSeconds
		}
		select {
		case <-time.After(b.dur(typing)):
		case <-ctx.Done():
			return
		}
		b.mu.Lock()
		// Владелец начал писать сам или собеседник прислал новое — остаток не шлём.
		if time.Since(st.lastOwner) < b.dur(typing)+2*time.Second || st.gen != gen {
			b.mu.Unlock()
			return
		}
		for k, t := range st.echoes {
			if time.Since(t) > time.Minute {
				delete(st.echoes, k)
			}
		}
		st.echoes[p] = time.Now()
		b.mu.Unlock()

		replyTo := 0
		if chat.IsGroup && i == 0 {
			replyTo = mentionID
		}
		if err := b.tg.Send(ctx, chat, p, replyTo); err != nil {
			log.Printf("[%s] отправка: %v", chat.Title, err)
			return
		}
		now := time.Now()
		b.mu.Lock()
		st.sent = append(st.sent, now)
		b.global = append(b.global, now)
		b.mu.Unlock()
		b.log(chat, "Я (бот)", true, true, p)
	}
	if len(parts) > 0 {
		b.mu.Lock()
		st.botTurns++
		b.mu.Unlock()
	}
}

func (b *Bot) notify(ctx context.Context, text string) {
	if !b.cfg.NotifyOwner || b.dryRun {
		log.Print(text)
		return
	}
	if err := b.tg.NotifyOwner(ctx, text); err != nil {
		log.Printf("уведомление: %v", err)
	}
}

// Busy — есть ли запланированная или идущая обработка хоть в одном чате.
func (b *Bot) Busy() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, st := range b.chats {
		if st.running || !st.burstStart.IsZero() {
			return true
		}
	}
	return false
}

// parroted — сколько частей ответа дословно повторяют входящие сообщения собеседника.
func parroted(msgs []string, lines []tgx.Line) int {
	in := map[string]bool{}
	for _, l := range lines {
		if !l.Out {
			in[privacy.Canon(strings.Trim(l.Text, " ?!."))] = true
		}
	}
	n := 0
	for _, m := range msgs {
		if c := privacy.Canon(strings.Trim(m, " ?!.")); c != "" && in[c] {
			n++
		}
	}
	return n
}

// LoadArchived подтягивает список архивных чатов. Список только пополняется:
// Telegram сам вынимает чат из архива при новом сообщении, и это не повод боту туда лезть.
// Чтобы открыть боту чат, разархивируй его и перезапусти бота.
func (b *Bot) LoadArchived(ctx context.Context) {
	if !b.cfg.SkipArchived {
		return
	}
	keys, err := b.tg.ArchivedKeys(ctx)
	if err != nil {
		log.Printf("архив: %v", err)
		return
	}
	b.mu.Lock()
	for k := range keys {
		b.archived[k] = true
	}
	n := len(b.archived)
	b.mu.Unlock()
	log.Printf("архивных чатов (бот их не трогает): %d", n)
}

// OnFolderPeers — чаты переместили между папками. В архив — сразу закрываем доступ.
func (b *Bot) OnFolderPeers(peers []tg.FolderPeer) {
	if !b.cfg.SkipArchived {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, fp := range peers {
		if k := tgx.PeerKey(fp.Peer); k != "" && fp.FolderID == tgx.ArchiveFolder {
			b.archived[k] = true
			if st, ok := b.chats[k]; ok && st.timer != nil {
				st.timer.Stop() // отменяем уже запланированный ответ
				st.burstStart = time.Time{}
			}
		}
	}
}

// Archived — ключи архивных чатов (для анализа стиля).
func (b *Bot) Archived() map[string]bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]bool, len(b.archived))
	for k := range b.archived {
		out[k] = true
	}
	return out
}

func (b *Bot) isArchived(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.archived[key]
}
