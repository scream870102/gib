package links

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
)

type Action string

const (
	ActionReply         Action = "reply"
	ActionDeleteRepost  Action = "delete-repost"
	ActionWebhookRepost Action = "webhook-repost"
	ActionEditOwn       Action = "edit-own"
)

type Config struct {
	Patterns    []string
	Action      Action
	WebhookName string
	BotName     string
}

type cleaner struct {
	res []*regexp.Regexp
}

type linkFeature struct {
	cleaner     *cleaner
	action      Action
	webhookName string
	webhookMu   sync.Mutex
	webhooks    map[string]*discordgo.Webhook
	logger      *slog.Logger
	config      Config
}

func Register(s *discordgo.Session, cfg Config, logger *slog.Logger) error {
	c, err := newCleaner(cfg.Patterns...)
	if err != nil {
		return err
	}

	feature := &linkFeature{
		cleaner:     c,
		action:      cfg.Action,
		webhookName: cfg.WebhookName,
		webhooks:    map[string]*discordgo.Webhook{},
		logger:      logger,
		config:      cfg,
	}

	s.AddHandler(feature.handleMessageCreate)
	return nil
}

func newCleaner(patterns ...string) (*cleaner, error) {
	res := make([]*regexp.Regexp, 0, len(patterns))
	for i, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("regex %d: %w", i+1, err)
		}
		if re.NumSubexp() < 1 {
			return nil, fmt.Errorf("regex %d must include capture group 1 for the cleaned URL", i+1)
		}
		res = append(res, re)
	}
	return &cleaner{res: res}, nil
}

func (c *cleaner) clean(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", false
	}

	for _, re := range c.res {
		newMessage := re.ReplaceAllString(trimmed, "${1}")
		newMessage = strings.TrimSpace(newMessage)
		if newMessage == "" || newMessage == trimmed {
			continue
		}
		return newMessage, true
	}

	return "", false
}

func (b *linkFeature) handleMessageCreate(s *discordgo.Session, event *discordgo.MessageCreate) {
	if event == nil || event.Author == nil {
		return
	}

	isSelf := s.State != nil && s.State.User != nil && event.Author.ID == s.State.User.ID
	if event.Author.Bot && !isSelf {
		return
	}
	if isSelf && b.action != ActionEditOwn {
		return
	}
	if !isSelf && b.action == ActionEditOwn {
		return
	}

	cleaned, ok := b.cleaner.clean(event.Content)
	if !ok {
		return
	}

	switch b.action {
	case ActionReply:
		b.replyWithCleanedLink(s, event, cleaned)
	case ActionDeleteRepost:
		b.deleteAndRepostCleanedLink(s, event, cleaned)
	case ActionWebhookRepost:
		b.deleteAndWebhookRepostCleanedLink(s, event, cleaned)
	case ActionEditOwn:
		b.editOwnMessage(s, event, cleaned)
	}
}

func (b *linkFeature) replyWithCleanedLink(s *discordgo.Session, event *discordgo.MessageCreate, cleaned string) {
	failIfNotExists := false
	_, err := s.ChannelMessageSendComplex(event.ChannelID, &discordgo.MessageSend{
		Content: cleaned,
		Reference: &discordgo.MessageReference{
			MessageID:       event.ID,
			ChannelID:       event.ChannelID,
			GuildID:         event.GuildID,
			FailIfNotExists: &failIfNotExists,
		},
		AllowedMentions: noMentions(),
	})
	if err != nil {
		b.logger.Error("send cleaned reply", "error", err)
		return
	}
}

func (b *linkFeature) deleteAndRepostCleanedLink(s *discordgo.Session, event *discordgo.MessageCreate, cleaned string) {
	_ = s.ChannelMessageDelete(event.ChannelID, event.ID)
	_, err := s.ChannelMessageSendComplex(event.ChannelID, &discordgo.MessageSend{
		Content:         cleaned,
		AllowedMentions: noMentions(),
	})
	if err != nil {
		b.logger.Error("repost cleaned link", "error", err)
	}
}

func (b *linkFeature) deleteAndWebhookRepostCleanedLink(s *discordgo.Session, event *discordgo.MessageCreate, cleaned string) {
	webhookChannelID, threadID, err := b.webhookTarget(s, event.ChannelID)
	if err != nil {
		return
	}

	webhook, err := b.webhookForChannel(s, webhookChannelID)
	if err != nil {
		return
	}

	_ = s.ChannelMessageDelete(event.ChannelID, event.ID)

	params := &discordgo.WebhookParams{
		Content:         cleaned,
		Username:        authorDisplayName(event, b.config.BotName),
		AvatarURL:       authorAvatarURL(event),
		AllowedMentions: noMentions(),
	}

	if threadID != "" {
		_, err = s.WebhookThreadExecute(webhook.ID, webhook.Token, true, threadID, params)
	} else {
		_, err = s.WebhookExecute(webhook.ID, webhook.Token, true, params)
	}
	if err != nil {
		b.logger.Error("webhook repost failed", "error", err)
	}
}

func (b *linkFeature) webhookTarget(s *discordgo.Session, channelID string) (string, string, error) {
	channel, err := s.Channel(channelID)
	if err != nil {
		return "", "", err
	}
	if channel.IsThread() && channel.ParentID != "" {
		return channel.ParentID, channel.ID, nil
	}
	return channelID, "", nil
}

func (b *linkFeature) webhookForChannel(s *discordgo.Session, channelID string) (*discordgo.Webhook, error) {
	b.webhookMu.Lock()
	defer b.webhookMu.Unlock()

	if wh, ok := b.webhooks[channelID]; ok {
		return wh, nil
	}

	webhooks, err := s.ChannelWebhooks(channelID)
	if err != nil {
		return nil, err
	}
	for _, wh := range webhooks {
		if wh.Name == b.webhookName {
			b.webhooks[channelID] = wh
			return wh, nil
		}
	}

	wh, err := s.WebhookCreate(channelID, b.webhookName, "")
	if err != nil {
		return nil, err
	}
	b.webhooks[channelID] = wh
	return wh, nil
}

func (b *linkFeature) editOwnMessage(s *discordgo.Session, event *discordgo.MessageCreate, cleaned string) {
	edit := discordgo.NewMessageEdit(event.ChannelID, event.ID)
	edit.SetContent(cleaned)
	edit.AllowedMentions = noMentions()
	_, _ = s.ChannelMessageEditComplex(edit)
}

func noMentions() *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}
}

func authorDisplayName(event *discordgo.MessageCreate, defaultName string) string {
	if event.Member != nil && event.Member.Nick != "" { return event.Member.Nick }
	if event.Author != nil { return event.Author.DisplayName() }
	return defaultName
}

func authorAvatarURL(event *discordgo.MessageCreate) string {
	if event.Author == nil { return "" }
	return event.Author.AvatarURL("")
}