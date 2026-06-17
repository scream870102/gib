package main

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestCleanerCleanInstagramIGSH(t *testing.T) {
	c, err := newCleaner(defaultCleanLinkRegexes...)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := c.clean("https://www.instagram.com/reel/DZjZmC1tF_P/?igsh=OGhueXc1emU5bTQ=")
	if !ok {
		t.Fatal("expected match")
	}

	want := "https://www.instagram.com/reel/DZjZmC1tF_P/"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCleanerCleanInstagramSourceAndIGSH(t *testing.T) {
	c, err := newCleaner(defaultCleanLinkRegexes...)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := c.clean("https://www.instagram.com/reel/DZq4Uc-Boi8/?utm_source=ig_web_copy_link&igsh=NTc4MTIwNjQ2YQ==")
	if !ok {
		t.Fatal("expected match")
	}

	want := "https://www.instagram.com/reel/DZq4Uc-Boi8/"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCleanerCleanTrimsWhitespace(t *testing.T) {
	c, err := newCleaner(defaultCleanLinkRegexes...)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := c.clean("  https://instagram.com/p/abc123/?igsh=A1b2C3_=+  ")
	if !ok {
		t.Fatal("expected match")
	}

	want := "https://instagram.com/p/abc123/"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCleanerRejectsNonIGSHLinks(t *testing.T) {
	c, err := newCleaner(defaultCleanLinkRegexes...)
	if err != nil {
		t.Fatal(err)
	}

	cases := []string{
		"https://www.instagram.com/reel/DZjZmC1tF_P/",
		"https://www.instagram.com/reel/DZjZmC1tF_P/?utm_source=test",
		"看這個 https://www.instagram.com/reel/DZjZmC1tF_P/?igsh=abc",
		"https://example.com/reel/DZjZmC1tF_P/?igsh=abc",
	}

	for _, tc := range cases {
		if got, ok := c.clean(tc); ok {
			t.Fatalf("clean(%q) matched unexpectedly with %q", tc, got)
		}
	}
}

func TestCleanerRequiresCaptureGroup(t *testing.T) {
	if _, err := newCleaner(`^https://example\.com$`); err == nil {
		t.Fatal("expected error")
	}
}

func TestCleanerRequiresCaptureGroupInEveryRegex(t *testing.T) {
	_, err := newCleaner(
		`^(https://example\.com/[a-z]+)\?tracking=[\x21-\x7E]+$`,
		`^https://example\.com/[a-z]+$`,
	)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCleanerAllowsCustomRegex(t *testing.T) {
	c, err := newCleaner(`^(https://example\.com/[a-z]+)\?tracking=[\x21-\x7E]+$`)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := c.clean("https://example.com/post?tracking=abc123")
	if !ok {
		t.Fatal("expected match")
	}

	want := "https://example.com/post"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCleanerAllowsMultipleCustomRegexes(t *testing.T) {
	c, err := newCleaner(
		`^(https://one\.example/[a-z]+)\?tracking=[\x21-\x7E]+$`,
		`^(https://two\.example/[a-z]+)\?ref=[\x21-\x7E]+$`,
	)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := c.clean("https://two.example/post?ref=abc123")
	if !ok {
		t.Fatal("expected match")
	}

	want := "https://two.example/post"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseCleanLinkPatternsAllowsJSONArray(t *testing.T) {
	patterns, err := parseCleanLinkPatterns(`["^one$","^two$"]`)
	if err != nil {
		t.Fatal(err)
	}

	if len(patterns) != 2 || patterns[0] != "^one$" || patterns[1] != "^two$" {
		t.Fatalf("got %#v", patterns)
	}
}

func TestAuthorDisplayNamePrefersGuildNickname(t *testing.T) {
	event := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			Author: &discordgo.User{
				Username:   "username",
				GlobalName: "global name",
			},
			Member: &discordgo.Member{
				Nick: "guild nick",
			},
		},
	}

	got := authorDisplayName(event, "test")
	if got != "guild nick" {
		t.Fatalf("got %q, want %q", got, "guild nick")
	}
}

func TestAuthorDisplayNameFallsBackToGlobalName(t *testing.T) {
	event := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			Author: &discordgo.User{
				Username:   "username",
				GlobalName: "global name",
			},
		},
	}

	got := authorDisplayName(event, "test")
	if got != "global name" {
		t.Fatalf("got %q, want %q", got, "global name")
	}
}

func TestTruncateRunesTrimsAndLimitsUnicodeNames(t *testing.T) {
	got := truncateRunes("  一二三四五  ", 3)
	if got != "一二三" {
		t.Fatalf("got %q, want %q", got, "一二三")
	}
}

func TestAuthorDisplayNameLimitsWebhookNameLength(t *testing.T) {
	event := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			Author: &discordgo.User{
				Username: strings.Repeat("a", 100),
			},
		},
	}

	got := authorDisplayName(event, "test")
	if len([]rune(got)) != 80 {
		t.Fatalf("got length %d, want 80", len([]rune(got)))
	}
}
