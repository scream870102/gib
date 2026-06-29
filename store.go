package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type guildConfig struct {
	GuildID   string            `json:"guild_id"`
	UpdatedAt time.Time         `json:"updated_at"`
	Mappings  map[string]string `json:"mappings"`
	Messages  map[string]string `json:"messages"`
}

type legacyGuildConfig struct {
	Mappings map[string]string `json:"mappings"`
	Messages map[string]string `json:"messages"`
}

type legacyStateData struct {
	Guilds map[string]*legacyGuildConfig `json:"guilds"`
}

type stateData struct {
	Guilds map[string]*guildConfig
}

type store struct {
	mu    sync.RWMutex
	dir   string
	data  stateData
	dirty map[string]struct{}
	clock func() time.Time
}

func loadStore(cfg reactionRoleConfig) (*store, error) {
	if cfg.StateDir == "" {
		return nil, errors.New("reaction role state dir is empty")
	}
	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return nil, err
	}

	s := &store{
		dir:   cfg.StateDir,
		data:  stateData{Guilds: map[string]*guildConfig{}},
		dirty: map[string]struct{}{},
		clock: time.Now,
	}

	hasShards, err := hasGuildShards(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	if !hasShards && cfg.LegacyStateFile != "" {
		if err := s.migrateLegacyFile(cfg.LegacyStateFile); err != nil {
			return nil, err
		}
	}
	if err := s.loadShards(); err != nil {
		return nil, err
	}
	return s, nil
}

func hasGuildShards(dir string) (bool, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return false, err
	}
	return len(matches) > 0, nil
}

func (s *store) migrateLegacyFile(path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}

	var legacy legacyStateData
	if err := json.Unmarshal(b, &legacy); err != nil {
		return fmt.Errorf("read legacy reaction role state: %w", err)
	}
	if len(legacy.Guilds) == 0 {
		return nil
	}

	updatedAt := s.clock().UTC()
	if info, err := os.Stat(path); err == nil {
		updatedAt = info.ModTime().UTC()
	}

	for guildID, legacyCfg := range legacy.Guilds {
		if strings.TrimSpace(guildID) == "" {
			continue
		}
		cfg := newGuildConfig(guildID, updatedAt)
		if legacyCfg != nil {
			for emojiKey, roleID := range legacyCfg.Mappings {
				cfg.Mappings[emojiKey] = roleID
			}
			for messageID, channelID := range legacyCfg.Messages {
				cfg.Messages[messageID] = channelID
			}
		}
		if err := s.writeGuild(cfg); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) loadShards() error {
	matches, err := filepath.Glob(filepath.Join(s.dir, "*.json"))
	if err != nil {
		return err
	}
	sort.Strings(matches)

	for _, path := range matches {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}

		var cfg guildConfig
		if err := json.Unmarshal(b, &cfg); err != nil {
			return fmt.Errorf("read reaction role shard %s: %w", path, err)
		}
		if cfg.GuildID == "" {
			cfg.GuildID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}
		if cfg.UpdatedAt.IsZero() {
			cfg.UpdatedAt = s.clock().UTC()
			if info, err := os.Stat(path); err == nil {
				cfg.UpdatedAt = info.ModTime().UTC()
			}
		}
		ensureGuildConfig(&cfg)
		s.data.Guilds[cfg.GuildID] = &cfg
	}
	return nil
}

func newGuildConfig(guildID string, updatedAt time.Time) *guildConfig {
	return &guildConfig{
		GuildID:   guildID,
		UpdatedAt: updatedAt.UTC(),
		Mappings:  map[string]string{},
		Messages:  map[string]string{},
	}
}

func ensureGuildConfig(cfg *guildConfig) {
	if cfg.Mappings == nil {
		cfg.Mappings = map[string]string{}
	}
	if cfg.Messages == nil {
		cfg.Messages = map[string]string{}
	}
}

func (s *store) guildLocked(guildID string) *guildConfig {
	if s.data.Guilds == nil {
		s.data.Guilds = map[string]*guildConfig{}
	}
	cfg := s.data.Guilds[guildID]
	if cfg == nil {
		cfg = newGuildConfig(guildID, s.clock().UTC())
		s.data.Guilds[guildID] = cfg
	}
	ensureGuildConfig(cfg)
	return cfg
}

func (s *store) guildPath(guildID string) (string, error) {
	if guildID == "" || strings.ContainsAny(guildID, `/\`) || guildID == "." || guildID == ".." {
		return "", fmt.Errorf("invalid guild id %q", guildID)
	}
	return filepath.Join(s.dir, guildID+".json"), nil
}

func (s *store) writeGuild(cfg *guildConfig) error {
	path, err := s.guildPath(cfg.GuildID)
	if err != nil {
		return err
	}
	ensureGuildConfig(cfg)
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func cloneGuildConfig(cfg *guildConfig) *guildConfig {
	if cfg == nil {
		return nil
	}
	clone := newGuildConfig(cfg.GuildID, cfg.UpdatedAt)
	for emojiKey, roleID := range cfg.Mappings {
		clone.Mappings[emojiKey] = roleID
	}
	for messageID, channelID := range cfg.Messages {
		clone.Messages[messageID] = channelID
	}
	return clone
}

func (s *store) saveGuildLocked(cfg *guildConfig) error {
	cfg.UpdatedAt = s.clock().UTC()
	if err := s.writeGuild(cfg); err != nil {
		return err
	}
	s.dirty[cfg.GuildID] = struct{}{}
	return nil
}

func (s *store) addMapping(guildID, emojiKey, roleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.guildLocked(guildID)
	cfg.Mappings[emojiKey] = roleID
	return s.saveGuildLocked(cfg)
}

func (s *store) removeMapping(guildID, emojiKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.guildLocked(guildID)
	delete(cfg.Mappings, emojiKey)
	return s.saveGuildLocked(cfg)
}

func (s *store) listMappings(guildID string) map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg := s.data.Guilds[guildID]
	if cfg == nil {
		return map[string]string{}
	}
	result := make(map[string]string, len(cfg.Mappings))
	for emojiKey, roleID := range cfg.Mappings {
		result[emojiKey] = roleID
	}
	return result
}

func (s *store) bindMessage(guildID, messageID, channelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.guildLocked(guildID)
	cfg.Messages[messageID] = channelID
	return s.saveGuildLocked(cfg)
}

func (s *store) unbindMessage(guildID, messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.guildLocked(guildID)
	delete(cfg.Messages, messageID)
	return s.saveGuildLocked(cfg)
}

func (s *store) roleForEmoji(guildID, emojiKey string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg := s.data.Guilds[guildID]
	if cfg == nil {
		return "", false
	}
	roleID, ok := cfg.Mappings[emojiKey]
	return roleID, ok
}

func (s *store) isDesignated(guildID, messageID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg := s.data.Guilds[guildID]
	if cfg == nil {
		return false
	}
	_, ok := cfg.Messages[messageID]
	return ok
}

func (s *store) messagesOf(guildID string) map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg := s.data.Guilds[guildID]
	if cfg == nil {
		return map[string]string{}
	}
	result := make(map[string]string, len(cfg.Messages))
	for messageID, channelID := range cfg.Messages {
		result[messageID] = channelID
	}
	return result
}

func (s *store) emojiKeys(guildID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg := s.data.Guilds[guildID]
	if cfg == nil {
		return nil
	}
	keys := make([]string, 0, len(cfg.Mappings))
	for emojiKey := range cfg.Mappings {
		keys = append(keys, emojiKey)
	}
	return keys
}

func (s *store) guildIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := make([]string, 0, len(s.data.Guilds))
	for guildID := range s.data.Guilds {
		ids = append(ids, guildID)
	}
	sort.Strings(ids)
	return ids
}

func (s *store) guildSnapshot(guildID string) (*guildConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg := s.data.Guilds[guildID]
	if cfg == nil {
		return nil, false
	}
	return cloneGuildConfig(cfg), true
}

func (s *store) replaceGuild(cfg *guildConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	clone := cloneGuildConfig(cfg)
	ensureGuildConfig(clone)
	s.data.Guilds[clone.GuildID] = clone
	return s.writeGuild(clone)
}

func (s *store) dirtyGuilds() []*guildConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	guildIDs := make([]string, 0, len(s.dirty))
	for guildID := range s.dirty {
		guildIDs = append(guildIDs, guildID)
	}
	sort.Strings(guildIDs)

	result := make([]*guildConfig, 0, len(guildIDs))
	for _, guildID := range guildIDs {
		if cfg := s.data.Guilds[guildID]; cfg != nil {
			result = append(result, cloneGuildConfig(cfg))
		}
	}
	return result
}

func (s *store) clearDirtyIfUnchanged(guildID string, updatedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.data.Guilds[guildID]
	if cfg == nil || !cfg.UpdatedAt.Equal(updatedAt) {
		return
	}
	delete(s.dirty, guildID)
}
