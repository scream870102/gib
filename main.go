package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"github.com/bwmarrin/discordgo"
)

const defaultWebhookName = "gib"
const defaultBotName = "gib"

var defaultCleanLinkRegexes = []string{}

type action string

const (
	actionReply         action = "reply"
	actionDeleteRepost  action = "delete-repost"
	actionWebhookRepost action = "webhook-repost"
	actionEditOwn       action = "edit-own"
)

type config struct {
	token       string
	patterns    []string
	action      action
	webhookName string
	botName     string
}

type cleaner struct {
	res []*regexp.Regexp
}

type bot struct {
	cleaner     *cleaner
	action      action
	webhookName string
	webhookMu   sync.Mutex
	webhooks    map[string]*discordgo.Webhook
	logger      *slog.Logger
	config      config
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid config", "error", err)
		os.Exit(1)
	}

	c, err := newCleaner(cfg.patterns...)
	if err != nil {
		logger.Error("invalid clean link regex", "error", err)
		os.Exit(1)
	}

	session, err := discordgo.New("Bot " + cfg.token)
	if err != nil {
		logger.Error("create discord session", "error", err)
		os.Exit(1)
	}

	session.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsMessageContent

	b := &bot{
		cleaner:     c,
		action:      cfg.action,
		webhookName: cfg.webhookName,
		webhooks:    map[string]*discordgo.Webhook{},
		logger:      logger,
		config:      cfg,
	}

	session.AddHandler(func(_ *discordgo.Session, ready *discordgo.Ready) {
		logger.Info("bot is ready", "user", ready.User.String(), "action", cfg.action)
	})
	session.AddHandler(b.handleMessageCreate)

	if err := session.Open(); err != nil {
		logger.Error("open discord gateway", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := session.Close(); err != nil {
			logger.Error("close discord session", "error", err)
		}
	}()

	logger.Info("listening for messages")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	logger.Info("shutdown requested")
}

func loadConfig() (config, error) {
	token := firstEnv("DISCORD_BOT_TOKEN", "DISCORD_TOKEN")
	if token == "" {
		return config{}, errors.New("set DISCORD_BOT_TOKEN")
	}

	rawAction := strings.ToLower(envOrDefault("BOT_ACTION", string(actionReply)))
	patterns, err := cleanLinkPatternsFromEnv()
	if err != nil {
		return config{}, err
	}

	cfg := config{
		token:       token,
		patterns:    patterns,
		action:      action(rawAction),
		webhookName: envOrDefault("WEBHOOK_NAME", defaultWebhookName),
		botName:     envOrDefault("DEFAULT_BOT_NAME", defaultBotName),
	}

	switch cfg.action {
	case actionReply, actionDeleteRepost, actionWebhookRepost, actionEditOwn:
		return cfg, nil
	default:
		return config{}, fmt.Errorf("BOT_ACTION must be one of %q, %q, %q, %q", actionReply, actionDeleteRepost, actionWebhookRepost, actionEditOwn)
	}
}

func cleanLinkPatternsFromEnv() ([]string, error) {
	if indexed := indexedCleanLinkPatternsFromEnv(); len(indexed) > 0 {
		return indexed, nil
	}

	raw := strings.TrimSpace(os.Getenv("CLEAN_LINK_REGEX"))
	if raw == "" {
		return append([]string(nil), defaultCleanLinkRegexes...), nil
	}
	return parseCleanLinkPatterns(raw)
}

func indexedCleanLinkPatternsFromEnv() []string {
	var patterns []string
	for i := 1; ; i++ {
		pattern := strings.TrimSpace(os.Getenv(fmt.Sprintf("CLEAN_LINK_REGEX_%d", i)))
		if pattern == "" {
			break
		}
		patterns = append(patterns, pattern)
	}
	return patterns
}

func parseCleanLinkPatterns(raw string) ([]string, error) {
	raw = strings.Trim(strings.TrimSpace(raw), "\"'")
	if raw == "" {
		return nil, errors.New("set CLEAN_LINK_REGEX with at least one regex")
	}

	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		var patterns []string
		if err := json.Unmarshal([]byte(raw), &patterns); err != nil {
			return nil, fmt.Errorf("parse CLEAN_LINK_REGEX JSON array: %w", err)
		}
		return normalizeCleanLinkPatterns(patterns)
	}

	return normalizeCleanLinkPatterns(strings.Split(raw, "\n"))
}

func normalizeCleanLinkPatterns(patterns []string) ([]string, error) {
	cleaned := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern != "" {
			cleaned = append(cleaned, pattern)
		}
	}
	if len(cleaned) == 0 {
		return nil, errors.New("set CLEAN_LINK_REGEX with at least one regex")
	}
	return cleaned, nil
}

func newCleaner(patterns ...string) (*cleaner, error) {
	patterns, err := normalizeCleanLinkPatterns(patterns)
	if err != nil {
		return nil, err
	}

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

func (b *bot) handleMessageCreate(s *discordgo.Session, event *discordgo.MessageCreate) {
	if event == nil || event.Author == nil {
		return
	}

	isSelf := s.State != nil && s.State.User != nil && event.Author.ID == s.State.User.ID
	if event.Author.Bot && !isSelf {
		return
	}
	if isSelf && b.action != actionEditOwn {
		return
	}
	if !isSelf && b.action == actionEditOwn {
		return
	}

	cleaned, ok := b.cleaner.clean(event.Content)
	if !ok {
		return
	}

	switch b.action {
	case actionReply:
		b.replyWithCleanedLink(s, event, cleaned)
	case actionDeleteRepost:
		b.deleteAndRepostCleanedLink(s, event, cleaned)
	case actionWebhookRepost:
		b.deleteAndWebhookRepostCleanedLink(s, event, cleaned)
	case actionEditOwn:
		b.editOwnMessage(s, event, cleaned)
	}
}

func (b *bot) replyWithCleanedLink(s *discordgo.Session, event *discordgo.MessageCreate, cleaned string) {
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
		b.logger.Error("send cleaned reply", "channel_id", event.ChannelID, "message_id", event.ID, "error", err)
		return
	}
	b.logger.Info("sent cleaned reply", "channel_id", event.ChannelID, "message_id", event.ID)
}

func (b *bot) deleteAndRepostCleanedLink(s *discordgo.Session, event *discordgo.MessageCreate, cleaned string) {
	if err := s.ChannelMessageDelete(event.ChannelID, event.ID); err != nil {
		b.logger.Error("delete original message", "channel_id", event.ChannelID, "message_id", event.ID, "error", err)
		return
	}

	_, err := s.ChannelMessageSendComplex(event.ChannelID, &discordgo.MessageSend{
		Content:         cleaned,
		AllowedMentions: noMentions(),
	})
	if err != nil {
		b.logger.Error("repost cleaned link", "channel_id", event.ChannelID, "message_id", event.ID, "error", err)
		return
	}
	b.logger.Info("deleted and reposted cleaned link", "channel_id", event.ChannelID, "message_id", event.ID)
}

func (b *bot) deleteAndWebhookRepostCleanedLink(s *discordgo.Session, event *discordgo.MessageCreate, cleaned string) {
	webhookChannelID, threadID, err := b.webhookTarget(s, event.ChannelID)
	if err != nil {
		b.logger.Error("resolve webhook target", "channel_id", event.ChannelID, "message_id", event.ID, "error", err)
		return
	}

	webhook, err := b.webhookForChannel(s, webhookChannelID)
	if err != nil {
		b.logger.Error("get or create webhook", "channel_id", webhookChannelID, "message_id", event.ID, "error", err)
		return
	}

	if err := s.ChannelMessageDelete(event.ChannelID, event.ID); err != nil {
		b.logger.Error("delete original message", "channel_id", event.ChannelID, "message_id", event.ID, "error", err)
		return
	}

	params := &discordgo.WebhookParams{
		Content:         cleaned,
		Username:        authorDisplayName(event, b.config.botName),
		AvatarURL:       authorAvatarURL(event),
		AllowedMentions: noMentions(),
	}

	if threadID != "" {
		_, err = s.WebhookThreadExecute(webhook.ID, webhook.Token, true, threadID, params)
	} else {
		_, err = s.WebhookExecute(webhook.ID, webhook.Token, true, params)
	}
	if err != nil {
		b.logger.Error("webhook repost cleaned link", "channel_id", event.ChannelID, "message_id", event.ID, "error", err)
		return
	}

	b.logger.Info("deleted and webhook-reposted cleaned link", "channel_id", event.ChannelID, "message_id", event.ID)
}

func (b *bot) webhookTarget(s *discordgo.Session, channelID string) (webhookChannelID string, threadID string, err error) {
	channel, err := channelFromStateOrAPI(s, channelID)
	if err != nil {
		return "", "", err
	}
	if channel != nil && channel.IsThread() && channel.ParentID != "" {
		return channel.ParentID, channel.ID, nil
	}
	return channelID, "", nil
}

func channelFromStateOrAPI(s *discordgo.Session, channelID string) (*discordgo.Channel, error) {
	if s.State != nil {
		if channel, err := s.State.Channel(channelID); err == nil {
			return channel, nil
		}
	}
	return s.Channel(channelID)
}

func (b *bot) webhookForChannel(s *discordgo.Session, channelID string) (*discordgo.Webhook, error) {
	b.webhookMu.Lock()
	defer b.webhookMu.Unlock()

	if webhook := b.webhooks[channelID]; webhook != nil && webhook.Token != "" {
		return webhook, nil
	}

	webhooks, err := s.ChannelWebhooks(channelID)
	if err != nil {
		return nil, err
	}
	for _, webhook := range webhooks {
		if webhook == nil || webhook.Token == "" {
			continue
		}
		if webhook.Type == discordgo.WebhookTypeIncoming && webhook.Name == b.webhookName {
			b.webhooks[channelID] = webhook
			return webhook, nil
		}
	}

	webhook, err := s.WebhookCreate(channelID, b.webhookName, "")
	if err != nil {
		return nil, err
	}
	if webhook == nil || webhook.Token == "" {
		return nil, errors.New("created webhook did not return a token")
	}
	b.webhooks[channelID] = webhook
	return webhook, nil
}

func (b *bot) editOwnMessage(s *discordgo.Session, event *discordgo.MessageCreate, cleaned string) {
	edit := discordgo.NewMessageEdit(event.ChannelID, event.ID)
	edit.SetContent(cleaned)
	edit.AllowedMentions = noMentions()

	if _, err := s.ChannelMessageEditComplex(edit); err != nil {
		b.logger.Error("edit own message", "channel_id", event.ChannelID, "message_id", event.ID, "error", err)
		return
	}
	b.logger.Info("edited own message", "channel_id", event.ChannelID, "message_id", event.ID)
}

func authorDisplayName(event *discordgo.MessageCreate, defaultName string) string {
	if event != nil && event.Member != nil {
		if name := strings.TrimSpace(event.Member.Nick); name != "" {
			return truncateRunes(name, 80)
		}
	}
	if event != nil && event.Author != nil {
		if name := strings.TrimSpace(event.Author.DisplayName()); name != "" {
			return truncateRunes(name, 80)
		}
		if name := strings.TrimSpace(event.Author.Username); name != "" {
			return truncateRunes(name, 80)
		}
	}
	return defaultName
}

func authorAvatarURL(event *discordgo.MessageCreate) string {
	if event == nil || event.Author == nil {
		return ""
	}

	if event.Member != nil && event.Member.Avatar != "" {
		member := *event.Member
		if member.User == nil {
			member.User = event.Author
		}
		if url := strings.TrimSpace(member.AvatarURL("")); url != "" {
			return url
		}
	}

	return strings.TrimSpace(event.Author.AvatarURL(""))
}

func truncateRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func noMentions() *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{
		Parse:       []discordgo.AllowedMentionType{},
		RepliedUser: false,
	}
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		value := strings.TrimSpace(os.Getenv(key))
		if value != "" {
			return value
		}
	}
	return ""
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}
