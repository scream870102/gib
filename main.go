package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"gib/internal/links"

	"github.com/bwmarrin/discordgo"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid config", "error", err)
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

	if err := links.Register(session, cfg.linkConfig, logger); err != nil {
		logger.Error("register link cleaner", "error", err)
		os.Exit(1)
	}

	session.AddHandler(func(_ *discordgo.Session, ready *discordgo.Ready) {
		logger.Info("bot is ready", "user", ready.User.String())
	})

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
