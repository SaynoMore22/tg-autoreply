package tgx

import (
	"context"
	"fmt"

	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/tg"
)

// ArchiveFolder — id папки «Архив» в Telegram.
const ArchiveFolder = 1

// PeerKey — ключ чата в том же формате, что Chat.Key (u123 / c123 / ch123).
func PeerKey(p tg.PeerClass) string {
	switch v := p.(type) {
	case *tg.PeerUser:
		return fmt.Sprintf("u%d", v.UserID)
	case *tg.PeerChat:
		return fmt.Sprintf("c%d", v.ChatID)
	case *tg.PeerChannel:
		return fmt.Sprintf("ch%d", v.ChannelID)
	}
	return ""
}

// ArchivedKeys возвращает ключи всех чатов из архива.
func (t TG) ArchivedKeys(ctx context.Context) (map[string]bool, error) {
	keys := map[string]bool{}
	err := dialogs.NewQueryBuilder(t.API).GetDialogs().FolderID(ArchiveFolder).BatchSize(100).
		ForEach(ctx, func(_ context.Context, e dialogs.Elem) error {
			if d, ok := e.Dialog.(*tg.Dialog); ok {
				if k := PeerKey(d.Peer); k != "" {
					keys[k] = true
				}
			}
			return nil
		})
	return keys, err
}
