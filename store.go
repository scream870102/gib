package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type guildConfig struct {
	Mappings map[string]string `json:"mappings"`
	Messages map[string]string `json:"messages"`
}

type stateData struct {
	Guilds map[string]*guildConfig `json:"guilds"`
}

type store struct {
	mu   sync.RWMutex
	path string
	data stateData
}

func loadStore(path string) (*store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	s := &store{
		path: path,
		data: stateData{Guilds: map[string]*guildConfig{}},
	}

	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, err
	}
	if s.data.Guilds == nil {
		s.data.Guilds = map[string]*guildConfig{}
	}
	for guildID, cfg := range s.data.Guilds {
		if cfg == nil {
			s.data.Guilds[guildID] = newGuildConfig()
			continue
		}
		ensureGuildConfig(cfg)
	}
	return s, nil
}

func newGuildConfig() *guildConfig {
	return &guildConfig{
		Mappings: map[string]string{},
		Messages: map[string]string{},
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
		cfg = newGuildConfig()
		s.data.Guilds[guildID] = cfg
	}
	ensureGuildConfig(cfg)
	return cfg
}

func (s *store) saveLocked() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0o644)
}

func (s *store) addMapping(guildID, emojiKey, roleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.guildLocked(guildID).Mappings[emojiKey] = roleID
	return s.saveLocked()
}

func (s *store) removeMapping(guildID, emojiKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.guildLocked(guildID).Mappings, emojiKey)
	return s.saveLocked()
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

	s.guildLocked(guildID).Messages[messageID] = channelID
	return s.saveLocked()
}

func (s *store) unbindMessage(guildID, messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.guildLocked(guildID).Messages, messageID)
	return s.saveLocked()
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
