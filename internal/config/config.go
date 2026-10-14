package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config — все настраиваемые параметры бота (config.json).
type Config struct {
	OwnerName string `json:"owner_name"`

	// provider: "ollama" — локальная модель (бесплатно, данные не уходят с компьютера), "anthropic" — Claude API.
	Provider     string `json:"provider"`
	OllamaURL    string `json:"ollama_url"`
	OllamaModel  string `json:"ollama_model"`
	OllamaNumCtx int    `json:"ollama_num_ctx"`
	OllamaThink  bool   `json:"ollama_think"`
	Model        string `json:"model"`
	ReplyEffort  string `json:"reply_effort"`
	RecapEffort  string `json:"recap_effort"`
	StyleEffort  string `json:"style_effort"`

	// Кому отвечать.
	ReplyPrivate           bool     `json:"reply_private"`
	ReplyInGroupsOnMention bool     `json:"reply_in_groups_on_mention"`
	OnlyContacts           bool     `json:"only_contacts"`
	Whitelist              []string `json:"whitelist"` // id или @username; если не пусто — отвечаем только им
	Blacklist              []string `json:"blacklist"` // id или @username; им не отвечаем никогда

	// Скорость.
	DebounceSeconds          float64 `json:"debounce_seconds"`
	TypingCharsPerSecond     float64 `json:"typing_chars_per_second"`
	MaxTypingSeconds         float64 `json:"max_typing_seconds"`
	PauseBetweenPartsSeconds float64 `json:"pause_between_parts_seconds"`
	// Серии сообщений: ждём паузу DebounceSeconds, но не дольше MaxWaitSeconds с первого сообщения серии;
	// пока собеседник печатает — ждём ещё TypingExtendSeconds.
	MaxWaitSeconds      float64 `json:"max_wait_seconds"`
	TypingExtendSeconds float64 `json:"typing_extend_seconds"`

	// Антиспам.
	MaxParts                int `json:"max_parts"`
	MaxCharsPerPart         int `json:"max_chars_per_part"`
	PerChatMaxPerHour       int `json:"per_chat_max_messages_per_hour"`
	GlobalMaxPerHour        int `json:"global_max_messages_per_hour"`
	MaxBotTurnsWithoutOwner int `json:"max_bot_turns_without_owner"`
	OwnerActivePauseMinutes int `json:"owner_active_pause_minutes"`
	// Флуд: больше FloodMessages входящих за FloodWindowSeconds — молчим FloodCooldownMinutes.
	FloodMessages          int `json:"flood_messages"`
	FloodWindowSeconds     int `json:"flood_window_seconds"`
	FloodCooldownMinutes   int `json:"flood_cooldown_minutes"`
	PerChatMaxModelPerHour int `json:"per_chat_max_model_calls_per_hour"`

	HistoryMessages  int  `json:"history_messages"`
	ReactionsEnabled bool `json:"reactions_enabled"`
	// Вероятность поставить реакцию вместе с текстовым ответом (живые люди делают это нечасто).
	ReactionWithReplyChance float64 `json:"reaction_with_reply_chance"`
	// Не давать обещаний и конкретного времени за владельца (приеду, скину, «в 9»).
	BlockPromises bool `json:"block_promises"`
	// Незнакомцы (не в контактах): "notify" — не отвечать, а сообщить тебе; "reply" — отвечать как всем.
	StrangerMode string `json:"stranger_mode"`
	// Не трогать чаты из папки «Архив»: не читать, не отвечать, не брать в анализ стиля.
	SkipArchived bool   `json:"skip_archived"`
	MarkRead     bool   `json:"mark_read"`
	NotifyOwner  bool   `json:"notify_owner_in_saved_messages"`
	BotMarker    string `json:"bot_marker"`
	AllowLinks   bool   `json:"allow_links"`

	RecapTime string `json:"recap_time"` // "23:30", локальное время

	// Анализ стиля.
	StyleChats            int    `json:"style_chats"`
	StyleMessagesPerChat  int    `json:"style_messages_per_chat"`
	StyleExamplesInPrompt int    `json:"style_examples_in_prompt"`
	SameChatExamples      int    `json:"same_chat_examples"`
	ChatProfileEffort     string `json:"chat_profile_effort"`
}

func Default() Config {
	return Config{
		OwnerName:                "Алексей",
		Provider:                 "ollama",
		OllamaURL:                "http://127.0.0.1:11434",
		OllamaModel:              "qwen3:14b",
		OllamaNumCtx:             16384,
		OllamaThink:              true,
		Model:                    "claude-opus-5",
		ReplyEffort:              "low",
		RecapEffort:              "medium",
		StyleEffort:              "high",
		ReplyPrivate:             true,
		ReplyInGroupsOnMention:   true,
		DebounceSeconds:          4,
		MaxWaitSeconds:           15,
		TypingExtendSeconds:      6,
		FloodMessages:            15,
		FloodWindowSeconds:       60,
		FloodCooldownMinutes:     15,
		PerChatMaxModelPerHour:   30,
		TypingCharsPerSecond:     30,
		MaxTypingSeconds:         3,
		PauseBetweenPartsSeconds: 1,
		MaxParts:                 3,
		MaxCharsPerPart:          200,
		PerChatMaxPerHour:        15,
		GlobalMaxPerHour:         80,
		MaxBotTurnsWithoutOwner:  8,
		OwnerActivePauseMinutes:  5,
		HistoryMessages:          30,
		ReactionsEnabled:         true,
		ReactionWithReplyChance:  0.3,
		BlockPromises:            true,
		StrangerMode:             "notify",
		SkipArchived:             true,
		MarkRead:                 true,
		NotifyOwner:              true,
		RecapTime:                "23:30",
		StyleChats:               30,
		StyleMessagesPerChat:     500,
		StyleExamplesInPrompt:    60,
		SameChatExamples:         25,
		ChatProfileEffort:        "medium",
	}
}

// Load читает config.json поверх значений по умолчанию. Если файла нет — создаёт его.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		out, _ := json.MarshalIndent(cfg, "", "  ")
		if werr := os.WriteFile(path, out, 0o600); werr != nil {
			return cfg, werr
		}
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// Matches проверяет, попадает ли пользователь в список (id или username, регистр не важен).
func Matches(list []string, id int64, username string) bool {
	ids := strconv.FormatInt(id, 10)
	u := strings.ToLower(strings.TrimPrefix(username, "@"))
	for _, item := range list {
		item = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(item, "@")))
		if item == "" {
			continue
		}
		if item == ids || (u != "" && item == u) {
			return true
		}
	}
	return false
}
