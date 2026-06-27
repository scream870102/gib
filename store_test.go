package main

import (
	"path/filepath"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "reactionroles.json")

	st, err := loadStore(path)
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

	reloaded, err := loadStore(path)
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

func TestStoreReturnsCopies(t *testing.T) {
	st, err := loadStore(filepath.Join(t.TempDir(), "reactionroles.json"))
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
