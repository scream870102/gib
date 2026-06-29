package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const defaultWebhookName = "gib"
const defaultBotName = "gib"

type config struct {
	token        string
	linkConfig   linkConfig
	reactionRole reactionRoleConfig
	webhookName  string
	botName      string
}

func loadConfig() (config, error) {
	token := firstEnv("DISCORD_BOT_TOKEN", "DISCORD_TOKEN")
	if token == "" {
		return config{}, errors.New("set DISCORD_BOT_TOKEN")
	}

	rawAction := strings.ToLower(envOrDefault("BOT_ACTION", string(ActionReply)))
	syncInterval, err := reactionRoleSyncIntervalFromEnv()
	if err != nil {
		return config{}, err
	}
	patterns, err := linkPatternsFromEnv()
	if err != nil {
		return config{}, err
	}

	cfg := config{
		token: token,
		linkConfig: linkConfig{
			Patterns:    patterns,
			Action:      linkAction(rawAction),
			WebhookName: envOrDefault("WEBHOOK_NAME", defaultWebhookName),
			BotName:     envOrDefault("DEFAULT_BOT_NAME", defaultBotName),
		},
		reactionRole: reactionRoleConfig{
			StateDir:          envOrDefault("REACTION_ROLE_STATE_DIR", "data/reactionroles"),
			LegacyStateFile:   envOrDefault("REACTION_ROLE_STATE_FILE", "data/reactionroles.json"),
			RemoteDatabaseURL: strings.TrimSpace(os.Getenv("REACTION_ROLE_REMOTE_DATABASE_URL")),
			SyncInterval:      syncInterval,
			CommandGuildID:    strings.TrimSpace(os.Getenv("COMMAND_GUILD_ID")),
		},
		webhookName: envOrDefault("WEBHOOK_NAME", defaultWebhookName),
		botName:     envOrDefault("DEFAULT_BOT_NAME", defaultBotName),
	}

	switch cfg.linkConfig.Action {
	case ActionReply, ActionDeleteRepost, ActionWebhookRepost, ActionEditOwn:
		return cfg, nil
	default:
		return config{}, fmt.Errorf("invalid BOT_ACTION")
	}
}

func reactionRoleSyncIntervalFromEnv() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("REACTION_ROLE_SYNC_INTERVAL"))
	if raw == "" {
		return 15 * time.Minute, nil
	}
	interval, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid REACTION_ROLE_SYNC_INTERVAL: %w", err)
	}
	if interval <= 0 {
		return 0, errors.New("REACTION_ROLE_SYNC_INTERVAL must be positive")
	}
	return interval, nil
}

func linkPatternsFromEnv() ([]string, error) {
	if indexed := indexedPatternsFromEnv(); len(indexed) > 0 {
		return indexed, nil
	}

	raw := strings.TrimSpace(os.Getenv("CLEAN_LINK_REGEX"))
	if raw == "" {
		return []string(nil), errors.New("not set CLEAN_LINK_REGEX")
	}
	return parsePatterns(raw)
}

func indexedPatternsFromEnv() []string {
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

func parsePatterns(raw string) ([]string, error) {
	raw = strings.Trim(strings.TrimSpace(raw), "\"'")
	if raw == "" {
		return nil, errors.New("set CLEAN_LINK_REGEX with at least one regex")
	}

	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		var patterns []string
		if err := json.Unmarshal([]byte(raw), &patterns); err != nil {
			return nil, fmt.Errorf("parse CLEAN_LINK_REGEX JSON array: %w", err)
		}
		return normalizePatterns(patterns)
	}

	return normalizePatterns(strings.Split(raw, "\n"))
}

func normalizePatterns(patterns []string) ([]string, error) {
	result := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern != "" {
			result = append(result, pattern)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("set CLEAN_LINK_REGEX with at least one regex")
	}
	return result, nil
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
