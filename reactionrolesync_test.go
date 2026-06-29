package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type fakeReactionRoleRemote struct {
	states map[string]*guildConfig
	puts   []string
	fail   bool
}

func (f *fakeReactionRoleRemote) Close() error                       { return nil }
func (f *fakeReactionRoleRemote) EnsureSchema(context.Context) error { return nil }
func (f *fakeReactionRoleRemote) ListGuildStates(context.Context) (map[string]*guildConfig, error) {
	result := map[string]*guildConfig{}
	for guildID, cfg := range f.states {
		result[guildID] = cloneGuildConfig(cfg)
	}
	return result, nil
}
func (f *fakeReactionRoleRemote) PutGuildState(_ context.Context, cfg *guildConfig) error {
	if f.fail {
		return errors.New("remote unavailable")
	}
	if f.states == nil {
		f.states = map[string]*guildConfig{}
	}
	f.states[cfg.GuildID] = cloneGuildConfig(cfg)
	f.puts = append(f.puts, cfg.GuildID)
	return nil
}

func TestReactionRoleStartupSyncComparesGuildUpdatedAt(t *testing.T) {
	st, err := loadStore(reactionRoleConfig{StateDir: filepath.Join(t.TempDir(), "reactionroles")})
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}

	older := time.Date(2026, 6, 28, 1, 0, 0, 0, time.UTC)
	middle := older.Add(time.Hour)
	newer := middle.Add(time.Hour)

	localOnly := newGuildConfig("guild-local-only", middle)
	localOnly.Mappings["😀"] = "local-role"
	if err := st.replaceGuild(localOnly); err != nil {
		t.Fatalf("replace local-only: %v", err)
	}
	localNewer := newGuildConfig("guild-local-newer", newer)
	localNewer.Mappings["😀"] = "local-newer-role"
	if err := st.replaceGuild(localNewer); err != nil {
		t.Fatalf("replace local-newer: %v", err)
	}
	localOlder := newGuildConfig("guild-remote-newer", older)
	localOlder.Mappings["😀"] = "old-role"
	if err := st.replaceGuild(localOlder); err != nil {
		t.Fatalf("replace local-older: %v", err)
	}

	remoteOnly := newGuildConfig("guild-remote-only", middle)
	remoteOnly.Mappings["🔥"] = "remote-role"
	remoteNewer := newGuildConfig("guild-remote-newer", newer)
	remoteNewer.Mappings["😀"] = "new-role"
	remoteOlder := newGuildConfig("guild-local-newer", older)
	remoteOlder.Mappings["😀"] = "remote-old-role"
	remote := &fakeReactionRoleRemote{states: map[string]*guildConfig{
		remoteOnly.GuildID:  remoteOnly,
		remoteNewer.GuildID: remoteNewer,
		remoteOlder.GuildID: remoteOlder,
	}}

	syncer := &reactionRoleSyncer{store: st}
	if err := syncer.syncStartup(context.Background(), remote); err != nil {
		t.Fatalf("syncStartup: %v", err)
	}

	if roleID, ok := st.roleForEmoji("guild-remote-only", "🔥"); !ok || roleID != "remote-role" {
		t.Fatalf("remote-only guild not pulled: %q, %v", roleID, ok)
	}
	if roleID, ok := st.roleForEmoji("guild-remote-newer", "😀"); !ok || roleID != "new-role" {
		t.Fatalf("remote-newer guild not pulled: %q, %v", roleID, ok)
	}
	if roleID, ok := remote.states["guild-local-newer"].Mappings["😀"]; !ok || roleID != "local-newer-role" {
		t.Fatalf("local-newer guild not pushed: %q, %v", roleID, ok)
	}
	if roleID, ok := remote.states["guild-local-only"].Mappings["😀"]; !ok || roleID != "local-role" {
		t.Fatalf("local-only guild not pushed: %q, %v", roleID, ok)
	}
}

func TestReactionRoleDirtySyncClearsOnlyOnSuccess(t *testing.T) {
	st, err := loadStore(reactionRoleConfig{StateDir: filepath.Join(t.TempDir(), "reactionroles")})
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	st.clock = func() time.Time { return time.Date(2026, 6, 28, 1, 0, 0, 0, time.UTC) }
	if err := st.addMapping("guild-1", "😀", "role-1"); err != nil {
		t.Fatalf("addMapping: %v", err)
	}

	remote := &fakeReactionRoleRemote{}
	syncer := &reactionRoleSyncer{store: st}
	if err := syncer.syncDirty(context.Background(), remote); err != nil {
		t.Fatalf("syncDirty success: %v", err)
	}
	if got := st.dirtyGuilds(); len(got) != 0 {
		t.Fatalf("dirty guilds after success = %d; want 0", len(got))
	}
	if !reflect.DeepEqual(remote.puts, []string{"guild-1"}) {
		t.Fatalf("remote puts = %#v; want guild-1", remote.puts)
	}

	st.clock = func() time.Time { return time.Date(2026, 6, 28, 2, 0, 0, 0, time.UTC) }
	if err := st.addMapping("guild-1", "🔥", "role-2"); err != nil {
		t.Fatalf("second addMapping: %v", err)
	}
	remote.fail = true
	if err := syncer.syncDirty(context.Background(), remote); err == nil {
		t.Fatal("syncDirty failure returned nil")
	}
	if got := st.dirtyGuilds(); len(got) != 1 || got[0].GuildID != "guild-1" {
		t.Fatalf("dirty guilds after failure = %#v; want guild-1 retained", got)
	}
}
