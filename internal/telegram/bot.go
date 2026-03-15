// Package telegram управляет ботом: публикует статьи и собирает реакции.
//
// go-telegram-bot-api v5 не поддерживает message_reaction updates (Bot API 7.0+),
// поэтому реакции собираются через raw HTTP polling с ручной десериализацией JSON.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"

	"github.com/alex-pyslar/Feedium/internal/config"
	"github.com/alex-pyslar/Feedium/internal/domain"
)

// ---- Telegram Bot API 7.0 raw types --------------------------------------

type rawGetUpdatesRequest struct {
	Offset         int      `json:"offset,omitempty"`
	Limit          int      `json:"limit,omitempty"`
	Timeout        int      `json:"timeout,omitempty"`
	AllowedUpdates []string `json:"allowed_updates"`
}

type rawGetUpdatesResponse struct {
	OK     bool        `json:"ok"`
	Result []RawUpdate `json:"result"`
}

// RawUpdate — полная структура Telegram Update, включая message_reaction.
type RawUpdate struct {
	UpdateID        int                      `json:"update_id"`
	MessageReaction *RawMessageReaction      `json:"message_reaction"`
}

type RawMessageReaction struct {
	Chat        RawChat           `json:"chat"`
	MessageID   int               `json:"message_id"`
	User        *RawUser          `json:"user"`
	Date        int64             `json:"date"`
	OldReaction []RawReactionType `json:"old_reaction"`
	NewReaction []RawReactionType `json:"new_reaction"`
}

type RawReactionType struct {
	Type  string `json:"type"`  // "emoji" or "custom_emoji"
	Emoji string `json:"emoji"` // populated when Type == "emoji"
}

type RawChat struct{ ID int64 `json:"id"` }
type RawUser struct{ ID int64 `json:"id"` }

// ReactionEvent — нормализованное событие изменения реакции.
type ReactionEvent struct {
	TelegramMsgID int
	ChatID        int64
	UserID        int64
	OldEmojis     []string
	NewEmojis     []string
	Date          time.Time
}

// ---- Bot -----------------------------------------------------------------

// Bot управляет соединением с Telegram.
type Bot struct {
	api     *tgbotapi.BotAPI
	cfg     config.TelegramConfig
	log     *zap.Logger
	httpCli *http.Client
	baseURL string
}

func NewBot(cfg config.TelegramConfig, log *zap.Logger) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("init telegram bot: %w", err)
	}

	// Сбрасываем webhook, чтобы getUpdates работал
	if _, err := api.Request(tgbotapi.DeleteWebhookConfig{DropPendingUpdates: false}); err != nil {
		log.Warn("delete webhook", zap.Error(err))
	}

	return &Bot{
		api:     api,
		cfg:     cfg,
		log:     log,
		httpCli: &http.Client{Timeout: time.Duration(cfg.UpdateTimeoutSecs+5) * time.Second},
		baseURL: fmt.Sprintf("https://api.telegram.org/bot%s", cfg.Token),
	}, nil
}

// PostArticle публикует статью в канал.
// imageData — байты изображения из MinIO (nil → текстовое сообщение).
// Если imageData передан — отправляется sendPhoto с подписью.
// Возвращает telegram message_id.
func (b *Bot) PostArticle(ctx context.Context, a domain.Article, imageData []byte) (int, error) {
	text := formatPost(a)

	var msgID int

	if len(imageData) > 0 {
		photo := tgbotapi.NewPhoto(b.cfg.ChannelID, tgbotapi.FileBytes{
			Name:  fmt.Sprintf("article_%d.jpg", a.ID),
			Bytes: imageData,
		})
		photo.Caption = text
		photo.ParseMode = tgbotapi.ModeHTML

		resp, err := b.api.Send(photo)
		if err != nil {
			// Фото не отправилось — пробуем без него
			b.log.Warn("send photo failed, falling back to text",
				zap.String("title", a.Title), zap.Error(err))
			return b.sendText(a, text)
		}
		msgID = resp.MessageID
	} else {
		var err error
		msgID, err = b.sendText(a, text)
		if err != nil {
			return 0, err
		}
	}

	b.log.Info("posted article",
		zap.String("title", a.Title),
		zap.Int("msg_id", msgID),
		zap.Float64("score", a.FinalScore),
		zap.Bool("with_photo", len(imageData) > 0),
	)
	return msgID, nil
}

func (b *Bot) sendText(a domain.Article, text string) (int, error) {
	msg := tgbotapi.NewMessage(b.cfg.ChannelID, text)
	msg.ParseMode = tgbotapi.ModeHTML
	msg.DisableWebPagePreview = false
	resp, err := b.api.Send(msg)
	if err != nil {
		return 0, fmt.Errorf("send message: %w", err)
	}
	return resp.MessageID, nil
}

// PollReactions запускает long-polling цикл, отправляя ReactionEvent в канал out.
// Персистентный offset передаётся снаружи через getOffset/setOffset колбэки.
func (b *Bot) PollReactions(
	ctx context.Context,
	out chan<- ReactionEvent,
	getOffset func(context.Context) (int, error),
	setOffset func(context.Context, int) error,
) {
	offset, err := getOffset(ctx)
	if err != nil {
		b.log.Warn("load telegram offset", zap.Error(err))
	}

	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		updates, nextOffset, err := b.rawGetUpdates(ctx, offset, 100)
		if err != nil {
			b.log.Warn("getUpdates", zap.Error(err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				backoff = min(backoff*2, 60*time.Second)
			}
			continue
		}
		backoff = time.Second

		for _, u := range updates {
			if u.MessageReaction == nil {
				continue
			}
			event := toReactionEvent(u.MessageReaction)
			select {
			case out <- event:
			case <-ctx.Done():
				return
			}
		}

		if nextOffset > offset {
			offset = nextOffset
			if err := setOffset(ctx, offset); err != nil {
				b.log.Warn("save telegram offset", zap.Error(err))
			}
		}
	}
}

// rawGetUpdates вызывает getUpdates через raw HTTP с allowed_updates=["message_reaction"].
func (b *Bot) rawGetUpdates(ctx context.Context, offset, limit int) ([]RawUpdate, int, error) {
	req := rawGetUpdatesRequest{
		Offset:         offset,
		Limit:          limit,
		Timeout:        b.cfg.UpdateTimeoutSecs,
		AllowedUpdates: []string{"message_reaction"},
	}
	body, _ := json.Marshal(req)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		b.baseURL+"/getUpdates",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := b.httpCli.Do(httpReq)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	var result rawGetUpdatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, 0, fmt.Errorf("decode getUpdates response: %w", err)
	}
	if !result.OK {
		return nil, 0, fmt.Errorf("telegram api returned ok=false")
	}

	nextOffset := offset
	for _, u := range result.Result {
		if u.UpdateID+1 > nextOffset {
			nextOffset = u.UpdateID + 1
		}
	}
	return result.Result, nextOffset, nil
}

// ---- helpers -------------------------------------------------------------

func toReactionEvent(r *RawMessageReaction) ReactionEvent {
	e := ReactionEvent{
		TelegramMsgID: r.MessageID,
		ChatID:        r.Chat.ID,
		Date:          time.Unix(r.Date, 0),
	}
	if r.User != nil {
		e.UserID = r.User.ID
	}
	for _, rt := range r.OldReaction {
		if rt.Type == "emoji" {
			e.OldEmojis = append(e.OldEmojis, rt.Emoji)
		}
	}
	for _, rt := range r.NewReaction {
		if rt.Type == "emoji" {
			e.NewEmojis = append(e.NewEmojis, rt.Emoji)
		}
	}
	return e
}

// formatPost формирует текст поста.
// Если есть Claude-сгенерированный summary — использует его.
// Иначе — fallback на description (обрезанное).
func formatPost(a domain.Article) string {
	if a.Summary != "" {
		// Summary уже содержит HTML-разметку от Claude
		return a.Summary + fmt.Sprintf("\n\n<a href=\"%s\">Читать далее →</a>", a.Link)
	}

	// Fallback: ручное форматирование
	title := html.EscapeString(a.Title)
	desc := a.Description
	if len([]rune(desc)) > 300 {
		desc = string([]rune(desc)[:300]) + "..."
	}
	desc = html.EscapeString(desc)
	return fmt.Sprintf("<b>%s</b>\n\n%s\n\n<a href=\"%s\">Читать далее →</a>", title, desc, a.Link)
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
