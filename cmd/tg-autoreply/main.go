package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/joho/godotenv"
	"golang.org/x/term"

	"github.com/SaynoMore22/tg-autoreply/internal/bot"
	"github.com/SaynoMore22/tg-autoreply/internal/config"
	"github.com/SaynoMore22/tg-autoreply/internal/llm"
	"github.com/SaynoMore22/tg-autoreply/internal/privacy"
	"github.com/SaynoMore22/tg-autoreply/internal/recap"
	"github.com/SaynoMore22/tg-autoreply/internal/store"
	"github.com/SaynoMore22/tg-autoreply/internal/style"
	"github.com/SaynoMore22/tg-autoreply/internal/tgx"
)

const dataDir = "data"

func main() {
	dry := flag.Bool("dry", false, "ничего не отправлять, только писать решения в лог")
	restyle := flag.Bool("restyle", false, "заново собрать профиль стиля")
	styleOnly := flag.Bool("style-only", false, "собрать профиль стиля и выйти")
	recapNow := flag.Bool("recap-now", false, "сразу сделать рекап с последнего рекапа и выйти")
	flag.Parse()

	_ = godotenv.Load()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatal(err)
	}
	cfg, err := config.Load("config.json")
	if err != nil {
		log.Fatal(err)
	}

	appID, err := strconv.Atoi(os.Getenv("TG_API_ID"))
	if err != nil || os.Getenv("TG_API_HASH") == "" || os.Getenv("TG_PHONE") == "" {
		log.Fatal("заполни TG_API_ID, TG_API_HASH и TG_PHONE в .env")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	st, err := store.Open(filepath.Join(dataDir, "log.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	model, err := llm.Open(cfg.Provider, cfg.Model, cfg.OllamaURL, cfg.OllamaModel, cfg.OllamaNumCtx, cfg.OllamaThink)
	if err != nil {
		log.Fatal(err)
	}
	dispatcher := tg.NewUpdateDispatcher()
	gaps := updates.New(updates.Config{Handler: dispatcher})
	client := telegram.NewClient(appID, os.Getenv("TG_API_HASH"), telegram.Options{
		SessionStorage: &session.FileStorage{Path: filepath.Join(dataDir, "session.json")},
		UpdateHandler:  gaps,
	})

	err = client.Run(ctx, func(ctx context.Context) error {
		flow := auth.NewFlow(terminalAuth{phone: os.Getenv("TG_PHONE")}, auth.SendCodeOptions{})
		if err := client.Auth().IfNecessary(ctx, flow); err != nil {
			return fmt.Errorf("авторизация: %w", err)
		}
		self, err := client.Self(ctx)
		if err != nil {
			return err
		}
		api := client.API()
		log.Printf("вошёл как %s (id %d)", self.FirstName, self.ID)

		messenger := tgx.TG{API: api}
		rc := &recap.Recapper{Store: st, LLM: model, TG: messenger, Effort: cfg.RecapEffort, Owner: cfg.OwnerName, Dir: dataDir, At: cfg.RecapTime, DryRun: *dry}
		if *recapNow {
			v, _ := st.Get("recap_cutoff")
			n, _ := strconv.ParseInt(v, 10, 64)
			return rc.Run(ctx, time.Unix(n, 0), time.Now(), "Рекап по запросу")
		}

		archived := map[string]bool{}
		if cfg.SkipArchived {
			if archived, err = messenger.ArchivedKeys(ctx); err != nil {
				return fmt.Errorf("список архивных чатов: %w", err)
			}
		}

		corpus, profile, err := style.Load(dataDir)
		if err != nil || corpus.Chats == nil || *restyle || *styleOnly {
			log.Printf("собираю профиль стиля по %d последним личным чатам...", cfg.StyleChats)
			corpus, err = style.Collect(ctx, api, self.ID, cfg.StyleChats, cfg.StyleMessagesPerChat, archived)
			if err != nil {
				return fmt.Errorf("сбор стиля: %w", err)
			}
			log.Printf("собрано %d своих сообщений, %d фрагментов диалогов; строю общий профиль...", corpus.Stats.Messages, len(corpus.Exchanges))
			profile, err = style.BuildProfile(ctx, model, cfg.StyleEffort, cfg.OwnerName, corpus)
			if err != nil {
				return fmt.Errorf("профиль стиля: %w", err)
			}
			// С каждым человеком — своя манера: отдельный профиль на каждый чат.
			for _, cs := range corpus.Chats {
				if cs.Stats.Messages < style.MinOwnMessages {
					continue
				}
				p, err := style.BuildChatProfile(ctx, model, cfg.ChatProfileEffort, cfg.OwnerName, profile, cs)
				if err != nil {
					log.Printf("профиль чата %s: %v", cs.Chat, err)
					continue
				}
				cs.Profile = p
				log.Printf("профиль общения: %s", cs.Chat)
			}
			if err := style.Save(dataDir, corpus, profile); err != nil {
				return err
			}
			log.Printf("профили сохранены в %s", dataDir)
		}
		if *styleOnly {
			return nil
		}

		corpus.Without(archived)

		public, _ := os.ReadFile("persona.md")
		contacts, _ := os.ReadFile("persona_contacts.md")
		guard := privacy.NewGuard(cfg.AllowLinks)
		if err := guard.LoadDenylist("private.txt"); err != nil {
			return fmt.Errorf("private.txt: %w", err)
		}
		persona := bot.Persona{Public: string(public), Contacts: string(contacts)}
		b := bot.New(ctx, cfg, messenger, model, st, self.ID, persona, profile, corpus, guard, *dry)
		var saveMu sync.Mutex
		b.OnChatStyle = func(cs *style.ChatStyle) {
			saveMu.Lock()
			defer saveMu.Unlock()
			corpus.Chats[cs.ChatKey] = cs
			if err := style.Save(dataDir, corpus, profile); err != nil {
				log.Printf("сохранение профиля чата: %v", err)
			}
		}
		b.Preload(ctx, 100)
		b.LoadArchived(ctx)
		// Раз в 15 минут подхватываем чаты, убранные в архив (на случай пропущенных апдейтов).
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(15 * time.Minute):
					b.LoadArchived(ctx)
				}
			}
		}()

		dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
			b.OnMessage(ctx, e, u.Message)
			return nil
		})
		dispatcher.OnNewChannelMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
			b.OnMessage(ctx, e, u.Message)
			return nil
		})
		dispatcher.OnFolderPeers(func(ctx context.Context, e tg.Entities, u *tg.UpdateFolderPeers) error {
			b.OnFolderPeers(u.FolderPeers)
			return nil
		})
		dispatcher.OnUserTyping(func(ctx context.Context, e tg.Entities, u *tg.UpdateUserTyping) error {
			b.OnTyping(u.UserID)
			return nil
		})

		go rc.Loop(ctx)

		mode := "БОЕВОЙ режим — отвечаю"
		if *dry {
			mode = "тестовый режим (-dry) — ничего не отправляю"
		}
		return gaps.Run(ctx, api, self.ID, updates.AuthOptions{
			OnStart: func(ctx context.Context) { log.Printf("запущен, %s. Ctrl+C — выход.", mode) },
		})
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

// terminalAuth — вход по коду из Telegram, пароль 2FA спрашивается скрытым вводом.
type terminalAuth struct{ phone string }

func (a terminalAuth) Phone(context.Context) (string, error) { return a.phone, nil }

func (terminalAuth) Password(context.Context) (string, error) {
	fmt.Print("Пароль двухфакторной защиты: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	return strings.TrimSpace(string(b)), err
}

func (terminalAuth) Code(context.Context, *tg.AuthSentCode) (string, error) {
	fmt.Print("Код из Telegram: ")
	code, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(code), err
}

func (terminalAuth) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return errors.New("аккаунт не зарегистрирован")
}

func (terminalAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("регистрация не поддерживается")
}
