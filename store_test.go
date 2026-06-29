package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reactionroles")

	st, err := loadStore(reactionRoleConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}

	if err := st.addMapping("guild-1", "😀", "role-1"); err != nil {
		t.Fatalf("addMapping unicode: %v", err)
	}
	if err := st.addMapping("guild-1", "party:123", "role-2"); err != nil {
		t.Fatalf("addMapping custom: %v", err)
	}
	if err := st.bindMessage("guild-1", "message-1", "channel-1"); err != nil {
		t.Fatalf("bindMessage: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "guild-1.json")); err != nil {
		t.Fatalf("guild shard was not written: %v", err)
	}
	if roleID, ok := st.roleForEmoji("guild-1", "😀"); !ok || roleID != "role-1" {
		t.Fatalf("roleForEmoji = %q, %v; want role-1, true", roleID, ok)
	}
	if !st.isDesignated("guild-1", "message-1") {
		t.Fatal("isDesignated = false; want true")
	}
	if got := st.listMappings("guild-1"); len(got) != 2 {
		t.Fatalf("listMappings len = %d; want 2", len(got))
	}
	if got := st.messagesOf("guild-1"); got["message-1"] != "channel-1" {
		t.Fatalf("messagesOf message-1 = %q; want channel-1", got["message-1"])
	}

	reloaded, err := loadStore(reactionRoleConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}
	if roleID, ok := reloaded.roleForEmoji("guild-1", "party:123"); !ok || roleID != "role-2" {
		t.Fatalf("reloaded roleForEmoji = %q, %v; want role-2, true", roleID, ok)
	}
	if !reloaded.isDesignated("guild-1", "message-1") {
		t.Fatal("reloaded isDesignated = false; want true")
	}

	if err := reloaded.removeMapping("guild-1", "😀"); err != nil {
		t.Fatalf("removeMapping: %v", err)
	}
	if _, ok := reloaded.roleForEmoji("guild-1", "😀"); ok {
		t.Fatal("removed mapping still exists")
	}
	if err := reloaded.unbindMessage("guild-1", "message-1"); err != nil {
		t.Fatalf("unbindMessage: %v", err)
	}
	if reloaded.isDesignated("guild-1", "message-1") {
		t.Fatal("unbound message is still designated")
	}
}

func TestStoreWritesOnlyChangedGuildShard(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reactionroles")
	st, err := loadStore(reactionRoleConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}

	t1 := time.Date(2026, 6, 28, 1, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	t3 := t2.Add(time.Minute)
	current := t1
	st.clock = func() time.Time { return current }

	if err := st.addMapping("guild-1", "😀", "role-1"); err != nil {
		t.Fatalf("addMapping guild-1: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "guild-1.json")); err != nil {
		t.Fatalf("guild-1 shard missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "guild-2.json")); !os.IsNotExist(err) {
		t.Fatalf("guild-2 shard exists before guild-2 changed: %v", err)
	}

	current = t2
	if err := st.addMapping("guild-2", "🔥", "role-2"); err != nil {
		t.Fatalf("addMapping guild-2: %v", err)
	}
	current = t3
	if err := st.addMapping("guild-1", "🎮", "role-3"); err != nil {
		t.Fatalf("second addMapping guild-1: %v", err)
	}

	guild1 := readGuildShard(t, filepath.Join(dir, "guild-1.json"))
	guild2 := readGuildShard(t, filepath.Join(dir, "guild-2.json"))
	if !guild1.UpdatedAt.Equal(t3) {
		t.Fatalf("guild-1 updated_at = %s; want %s", guild1.UpdatedAt, t3)
	}
	if !guild2.UpdatedAt.Equal(t2) {
		t.Fatalf("guild-2 updated_at = %s; want %s", guild2.UpdatedAt, t2)
	}
}

func TestStoreReturnsCopies(t *testing.T) {
	st, err := loadStore(reactionRoleConfig{StateDir: filepath.Join(t.TempDir(), "reactionroles")})
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	if err := st.addMapping("guild-1", "😀", "role-1"); err != nil {
		t.Fatalf("addMapping: %v", err)
	}
	if err := st.bindMessage("guild-1", "message-1", "channel-1"); err != nil {
		t.Fatalf("bindMessage: %v", err)
	}

	mappings := st.listMappings("guild-1")
	mappings["😀"] = "mutated"
	messages := st.messagesOf("guild-1")
	messages["message-1"] = "mutated"

	if roleID, _ := st.roleForEmoji("guild-1", "😀"); roleID != "role-1" {
		t.Fatalf("store mapping was mutated through copy: %q", roleID)
	}
	if got := st.messagesOf("guild-1")["message-1"]; got != "channel-1" {
		t.Fatalf("store message was mutated through copy: %q", got)
	}
}

func TestStoreMigratesLegacyFileWhenNoShardsExist(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "reactionroles")
	legacyPath := filepath.Join(root, "reactionroles.json")
	legacy := `{"guilds":{"guild-1":{"mappings":{"😀":"role-1"},"messages":{"message-1":"channel-1"}},"guild-2":{"mappings":{"🔥":"role-2"},"messages":{}}}}`
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}

	st, err := loadStore(reactionRoleConfig{StateDir: dir, LegacyStateFile: legacyPath})
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	if roleID, ok := st.roleForEmoji("guild-1", "😀"); !ok || roleID != "role-1" {
		t.Fatalf("migrated roleForEmoji = %q, %v; want role-1, true", roleID, ok)
	}
	if _, err := os.Stat(filepath.Join(dir, "guild-1.json")); err != nil {
		t.Fatalf("guild-1 shard missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "guild-2.json")); err != nil {
		t.Fatalf("guild-2 shard missing: %v", err)
	}
}

func TestStoreDoesNotMigrateLegacyFileWhenShardsExist(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "reactionroles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir shards: %v", err)
	}
	existing := newGuildConfig("guild-1", time.Date(2026, 6, 28, 1, 0, 0, 0, time.UTC))
	existing.Mappings["😀"] = "existing-role"
	writeGuildShard(t, filepath.Join(dir, "guild-1.json"), existing)

	legacyPath := filepath.Join(root, "reactionroles.json")
	legacy := `{"guilds":{"guild-1":{"mappings":{"😀":"legacy-role"},"messages":{}},"guild-2":{"mappings":{"🔥":"role-2"},"messages":{}}}}`
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}

	st, err := loadStore(reactionRoleConfig{StateDir: dir, LegacyStateFile: legacyPath})
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	if roleID, ok := st.roleForEmoji("guild-1", "😀"); !ok || roleID != "existing-role" {
		t.Fatalf("roleForEmoji = %q, %v; want existing-role, true", roleID, ok)
	}
	if _, ok := st.roleForEmoji("guild-2", "🔥"); ok {
		t.Fatal("legacy guild-2 should not be migrated when shards already exist")
	}
}

func readGuildShard(t *testing.T, path string) guildConfig {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read shard %s: %v", path, err)
	}
	var cfg guildConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("decode shard %s: %v", path, err)
	}
	return cfg
}

func writeGuildShard(t *testing.T, path string, cfg *guildConfig) {
	t.Helper()
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal shard: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write shard %s: %v", path, err)
	}
}
