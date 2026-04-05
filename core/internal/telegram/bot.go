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

// ---- Telegram Bot API 7.0 raw types (message_reaction) --------------------

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

type RawUpdate struct {
	UpdateID        int                 `json:"update_id"`
	MessageReaction *RawMessageReaction `json:"message_reaction"`
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
	Type  string `json:"type"`
	Emoji string `json:"emoji"`
}

type RawChat struct{ ID int64 `json:"id"` }
type RawUser struct{ ID int64 `json:"id"` }

type ReactionEvent struct {
	TelegramMsgID int
	ChatID        int64
	UserID        int64
	OldEmojis     []string
	NewEmojis     []string
	Date          time.Time
}

// ---- Bot ------------------------------------------------------------------

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

// PostArticle публикует статью текстовым сообщением (изображения убраны).
// imageData оставлен в сигнатуре для совместимости, но игнорируется.
func (b *Bot) PostArticle(ctx context.Context, a domain.Article, _ []byte) (int, error) {
	text := formatPost(a)
	msg := tgbotapi.NewMessage(b.cfg.ChannelID, text)
	msg.ParseMode = tgbotapi.ModeHTML
	msg.DisableWebPagePreview = false
	resp, err := b.api.Send(msg)
	if err != nil {
		return 0, fmt.Errorf("send message: %w", err)
	}
	b.log.Info("posted article",
		zap.String("title", a.Title),
		zap.Int("msg_id", resp.MessageID),
		zap.Float64("score", a.FinalScore),
	)
	return resp.MessageID, nil
}

// PollReactions запускает long-polling реакций (Bot API 7.0+).
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
				if backoff < 60*time.Second {
					backoff *= 2
				}
			}
			continue
		}
		backoff = time.Second

		for _, u := range updates {
			if u.MessageReaction == nil {
				continue
			}
			select {
			case out <- toReactionEvent(u.MessageReaction):
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

func (b *Bot) rawGetUpdates(ctx context.Context, offset, limit int) ([]RawUpdate, int, error) {
	body, _ := json.Marshal(rawGetUpdatesRequest{
		Offset:         offset,
		Limit:          limit,
		Timeout:        b.cfg.UpdateTimeoutSecs,
		AllowedUpdates: []string{"message_reaction"},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/getUpdates", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.httpCli.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	var result rawGetUpdatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, 0, fmt.Errorf("decode getUpdates: %w", err)
	}
	if !result.OK {
		return nil, 0, fmt.Errorf("telegram api ok=false")
	}

	nextOffset := offset
	for _, u := range result.Result {
		if u.UpdateID+1 > nextOffset {
			nextOffset = u.UpdateID + 1
		}
	}
	return result.Result, nextOffset, nil
}

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

func formatPost(a domain.Article) string {
	title := html.EscapeString(a.Title)
	desc := a.Description
	if len([]rune(desc)) > 300 {
		desc = string([]rune(desc)[:300]) + "..."
	}
	desc = html.EscapeString(desc)
	return fmt.Sprintf("<b>%s</b>\n\n%s\n\n<a href=\"%s\">Читать далее →</a>", title, desc, a.Link)
}
