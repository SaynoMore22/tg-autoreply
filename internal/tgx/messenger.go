package tgx

import (
	"context"

	"github.com/gotd/td/tg"
)

// Messenger — всё, что боту нужно от Telegram. Реальная реализация — TG,
// в тестах и симуляторе подставляется фейк.
type Messenger interface {
	History(ctx context.Context, chat Chat, limit, offsetID int) ([]Line, error)
	Send(ctx context.Context, chat Chat, text string, replyTo int) error
	React(ctx context.Context, chat Chat, msgID int, emoji string) error
	Typing(ctx context.Context, chat Chat)
	MarkRead(ctx context.Context, chat Chat, maxID int)
	NotifyOwner(ctx context.Context, text string) error
	Dialogs(ctx context.Context, limit int) (tg.Entities, error)
	// ArchivedKeys — чаты в папке «Архив» (к ним у бота нет доступа).
	ArchivedKeys(ctx context.Context) (map[string]bool, error)
}

// TG — Messenger поверх настоящего MTProto-клиента.
type TG struct{ API *tg.Client }

func (t TG) History(ctx context.Context, c Chat, limit, offsetID int) ([]Line, error) {
	return History(ctx, t.API, c.Peer, limit, offsetID)
}

func (t TG) Send(ctx context.Context, c Chat, text string, replyTo int) error {
	return SendText(ctx, t.API, c.Peer, text, replyTo)
}

func (t TG) React(ctx context.Context, c Chat, msgID int, emoji string) error {
	return React(ctx, t.API, c.Peer, msgID, emoji)
}

func (t TG) Typing(ctx context.Context, c Chat) { Typing(ctx, t.API, c.Peer) }

func (t TG) MarkRead(ctx context.Context, c Chat, maxID int) { MarkRead(ctx, t.API, c, maxID) }

func (t TG) NotifyOwner(ctx context.Context, text string) error { return SendToSelf(ctx, t.API, text) }

func (t TG) Dialogs(ctx context.Context, limit int) (tg.Entities, error) {
	e := tg.Entities{Users: map[int64]*tg.User{}, Chats: map[int64]*tg.Chat{}, Channels: map[int64]*tg.Channel{}}
	res, err := t.API.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: limit})
	if err != nil {
		return e, err
	}
	mod, ok := res.AsModified()
	if !ok {
		return e, nil
	}
	for _, u := range mod.GetUsers() {
		if uu, ok := u.(*tg.User); ok {
			e.Users[uu.ID] = uu
		}
	}
	for _, c := range mod.GetChats() {
		switch cc := c.(type) {
		case *tg.Chat:
			e.Chats[cc.ID] = cc
		case *tg.Channel:
			e.Channels[cc.ID] = cc
		}
	}
	return e, nil
}
