// Package tgx — тонкие обёртки над сырым MTProto API gotd.
package tgx

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// Chat описывает диалог, в который пришло сообщение.
type Chat struct {
	Key     string // u123 / c123 / ch123
	Title   string
	Peer    tg.InputPeerClass
	Channel *tg.InputChannel // для супергрупп (нужно для ReadHistory)
	IsGroup bool
	User    *tg.User // для личных чатов
}

func RandomID() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int64(binary.LittleEndian.Uint64(b[:]))
}

func UserName(u *tg.User) string {
	if u == nil {
		return "?"
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" && u.Username != "" {
		name = "@" + u.Username
	}
	if name == "" {
		name = fmt.Sprintf("id%d", u.ID)
	}
	return name
}

// ResolveChat собирает Chat по peer сообщения и сущностям апдейта.
// ok=false — это канал-вещалка или неизвестный peer.
func ResolveChat(peer tg.PeerClass, users map[int64]*tg.User, chats map[int64]*tg.Chat, channels map[int64]*tg.Channel) (Chat, bool) {
	switch p := peer.(type) {
	case *tg.PeerUser:
		u := users[p.UserID]
		if u == nil {
			return Chat{}, false
		}
		return Chat{
			Key:   fmt.Sprintf("u%d", u.ID),
			Title: UserName(u),
			Peer:  &tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash},
			User:  u,
		}, true
	case *tg.PeerChat:
		c := chats[p.ChatID]
		title := fmt.Sprintf("chat%d", p.ChatID)
		if c != nil {
			title = c.Title
		}
		return Chat{
			Key:     fmt.Sprintf("c%d", p.ChatID),
			Title:   title,
			Peer:    &tg.InputPeerChat{ChatID: p.ChatID},
			IsGroup: true,
		}, true
	case *tg.PeerChannel:
		c := channels[p.ChannelID]
		if c == nil || c.Broadcast || !c.Megagroup {
			return Chat{}, false
		}
		return Chat{
			Key:     fmt.Sprintf("ch%d", c.ID),
			Title:   c.Title,
			Peer:    &tg.InputPeerChannel{ChannelID: c.ID, AccessHash: c.AccessHash},
			Channel: &tg.InputChannel{ChannelID: c.ID, AccessHash: c.AccessHash},
			IsGroup: true,
		}, true
	}
	return Chat{}, false
}

// Describe превращает сообщение в строку для модели: текст + пометки о вложениях.
func Describe(m *tg.Message) string {
	text := strings.TrimSpace(m.Message)
	media := ""
	switch md := m.Media.(type) {
	case nil:
	case *tg.MessageMediaPhoto:
		media = "[фото]"
	case *tg.MessageMediaDocument:
		media = "[файл]"
		if doc, ok := md.Document.(*tg.Document); ok {
			for _, a := range doc.Attributes {
				switch at := a.(type) {
				case *tg.DocumentAttributeSticker:
					media = "[стикер " + at.Alt + "]"
				case *tg.DocumentAttributeAudio:
					if at.Voice {
						media = "[голосовое сообщение]"
					} else {
						media = "[аудио]"
					}
				case *tg.DocumentAttributeVideo:
					if at.RoundMessage {
						media = "[видеокружок]"
					} else if media == "[файл]" {
						media = "[видео]"
					}
				case *tg.DocumentAttributeAnimated:
					media = "[гифка]"
				}
			}
		}
	case *tg.MessageMediaWebPage:
		// ссылка уже есть в тексте
	case *tg.MessageMediaGeo, *tg.MessageMediaGeoLive, *tg.MessageMediaVenue:
		media = "[геолокация]"
	case *tg.MessageMediaPoll:
		media = "[опрос]"
	case *tg.MessageMediaContact:
		media = "[контакт]"
	default:
		media = "[вложение]"
	}
	if m.FwdFrom.Date != 0 {
		media = strings.TrimSpace("[переслано] " + media)
	}
	switch {
	case media != "" && text != "":
		return media + " " + text
	case media != "":
		return media
	default:
		return text
	}
}

// Line — одно сообщение истории в удобном виде.
type Line struct {
	ID     int
	Out    bool
	Sender string
	Text   string
	At     time.Time
}

// History возвращает последние limit сообщений диалога (от старых к новым).
func History(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, limit, offsetID int) ([]Line, error) {
	res, err := call(ctx, func() (tg.MessagesMessagesClass, error) {
		return api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, Limit: limit, OffsetID: offsetID})
	})
	if err != nil {
		return nil, err
	}
	mod, ok := res.AsModified()
	if !ok {
		return nil, nil
	}
	users := map[int64]*tg.User{}
	for _, u := range mod.GetUsers() {
		if uu, ok := u.(*tg.User); ok {
			users[uu.ID] = uu
		}
	}
	var lines []Line
	for _, mc := range mod.GetMessages() {
		m, ok := mc.(*tg.Message)
		if !ok {
			continue
		}
		text := Describe(m)
		if text == "" {
			continue
		}
		sender := ""
		if from, ok := m.FromID.(*tg.PeerUser); ok {
			sender = UserName(users[from.UserID])
		} else if p, ok := m.PeerID.(*tg.PeerUser); ok && !m.Out {
			sender = UserName(users[p.UserID])
		}
		lines = append(lines, Line{ID: m.ID, Out: m.Out, Sender: sender, Text: text, At: time.Unix(int64(m.Date), 0)})
	}
	// API отдаёт от новых к старым.
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines, nil
}

func SendText(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, text string, replyTo int) error {
	req := &tg.MessagesSendMessageRequest{Peer: peer, Message: text, RandomID: RandomID()}
	if replyTo != 0 {
		req.ReplyTo = &tg.InputReplyToMessage{ReplyToMsgID: replyTo}
	}
	_, err := call(ctx, func() (tg.UpdatesClass, error) { return api.MessagesSendMessage(ctx, req) })
	return err
}

func React(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, msgID int, emoji string) error {
	_, err := call(ctx, func() (tg.UpdatesClass, error) {
		return api.MessagesSendReaction(ctx, &tg.MessagesSendReactionRequest{
			Peer:     peer,
			MsgID:    msgID,
			Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: emoji}},
		})
	})
	return err
}

func Typing(ctx context.Context, api *tg.Client, peer tg.InputPeerClass) {
	_, _ = api.MessagesSetTyping(ctx, &tg.MessagesSetTypingRequest{Peer: peer, Action: &tg.SendMessageTypingAction{}})
}

func MarkRead(ctx context.Context, api *tg.Client, c Chat, maxID int) {
	if c.Channel != nil {
		_, _ = api.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{Channel: c.Channel, MaxID: maxID})
		return
	}
	_, _ = api.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{Peer: c.Peer, MaxID: maxID})
}

// SendToSelf отправляет текст в «Избранное», разбивая на куски.
func SendToSelf(ctx context.Context, api *tg.Client, text string) error {
	const chunk = 3800
	r := []rune(text)
	for len(r) > 0 {
		n := min(chunk, len(r))
		if err := SendText(ctx, api, &tg.InputPeerSelf{}, string(r[:n]), 0); err != nil {
			return err
		}
		r = r[n:]
	}
	return nil
}

// call выполняет запрос, переживая один FLOOD_WAIT.
func call[T any](ctx context.Context, f func() (T, error)) (T, error) {
	res, err := f()
	if d, ok := tgerr.AsFloodWait(err); ok && d < 2*time.Minute {
		select {
		case <-time.After(d + time.Second):
		case <-ctx.Done():
			return res, ctx.Err()
		}
		return f()
	}
	return res, err
}
