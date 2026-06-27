package main

import "testing"

func TestParseEmojiInput(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		key  string
		ok   bool
	}{
		{name: "unicode", raw: " 😀 ", key: "😀", ok: true},
		{name: "custom", raw: "<:party:1234567890>", key: "party:1234567890", ok: true},
		{name: "animated custom", raw: "<a:dance_2:9876543210>", key: "dance_2:9876543210", ok: true},
		{name: "empty", raw: "  ", key: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, ok := parseEmojiInput(tt.raw)
			if key != tt.key || ok != tt.ok {
				t.Fatalf("parseEmojiInput(%q) = %q, %v; want %q, %v", tt.raw, key, ok, tt.key, tt.ok)
			}
		})
	}
}

func TestParseMessageRef(t *testing.T) {
	tests := []struct {
		name              string
		raw               string
		fallbackChannelID string
		channelID         string
		messageID         string
		ok                bool
	}{
		{
			name:      "full link",
			raw:       "https://discord.com/channels/111/222/333",
			channelID: "222",
			messageID: "333",
			ok:        true,
		},
		{
			name:              "message id",
			raw:               "333",
			fallbackChannelID: "222",
			channelID:         "222",
			messageID:         "333",
			ok:                true,
		},
		{
			name: "invalid",
			raw:  "not-a-message",
			ok:   false,
		},
		{
			name:      "message id without fallback",
			raw:       "333",
			channelID: "",
			messageID: "",
			ok:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channelID, messageID, ok := parseMessageRef(tt.raw, tt.fallbackChannelID)
			if channelID != tt.channelID || messageID != tt.messageID || ok != tt.ok {
				t.Fatalf("parseMessageRef(%q) = %q, %q, %v; want %q, %q, %v", tt.raw, channelID, messageID, ok, tt.channelID, tt.messageID, tt.ok)
			}
		})
	}
}
