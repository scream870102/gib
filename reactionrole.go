package main

import (
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

const reactionRoleCommandName = "reactionrole"

var (
	customEmojiPattern = regexp.MustCompile(`^<a?:([A-Za-z0-9_]+):(\d+)>$`)
	messageLinkPattern = regexp.MustCompile(`channels/\d+/(\d+)/(\d+)`)
	snowflakePattern   = regexp.MustCompile(`^\d+$`)
)

type reactionRoleConfig struct {
	StateDir          string
	LegacyStateFile   string
	RemoteDatabaseURL string
	SyncInterval      time.Duration
	CommandGuildID    string
}

type reactionRole struct {
	store          *store
	logger         *slog.Logger
	commandGuildID string
}

func reactionRoleFeatureRegister(s *discordgo.Session, cfg reactionRoleConfig, logger *slog.Logger) error {
	st, err := loadStore(cfg)
	if err != nil {
		return err
	}

	startReactionRoleSyncer(st, cfg, logger)

	rr := &reactionRole{
		store:          st,
		logger:         logger,
		commandGuildID: cfg.CommandGuildID,
	}

	s.AddHandler(rr.handleReactionAdd)
	s.AddHandler(rr.handleReactionRemove)
	s.AddHandler(rr.handleInteraction)
	s.AddHandler(func(s *discordgo.Session, _ *discordgo.Ready) {
		if err := rr.registerCommands(s); err != nil {
			rr.logger.Error("register reaction role commands", "error", err)
		}
	})
	return nil
}

func (rr *reactionRole) registerCommands(s *discordgo.Session) error {
	if s.State == nil || s.State.User == nil {
		return fmt.Errorf("missing bot user in session state")
	}

	_, err := s.ApplicationCommandCreate(s.State.User.ID, rr.commandGuildID, reactionRoleCommand())
	return err
}

func reactionRoleCommand() *discordgo.ApplicationCommand {
	manage := int64(discordgo.PermissionManageRoles)
	dmFalse := false
	return &discordgo.ApplicationCommand{
		Name:                     reactionRoleCommandName,
		Description:              "管理表情符號身分組",
		DefaultMemberPermissions: &manage,
		DMPermission:             &dmFalse,
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "map-add",
				Description: "新增或更新表情符號到身分組的對應",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "emoji",
						Description: "表情符號",
						Required:    true,
					},
					{
						Type:        discordgo.ApplicationCommandOptionRole,
						Name:        "role",
						Description: "要發放的身分組",
						Required:    true,
					},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "map-remove",
				Description: "移除表情符號到身分組的對應",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "emoji",
						Description: "表情符號",
						Required:    true,
					},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "list",
				Description: "列出表情符號身分組對應",
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "bind",
				Description: "指定一則訊息為反應面板",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "message",
						Description: "訊息連結或訊息 ID",
						Required:    true,
					},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "unbind",
				Description: "取消指定反應面板",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "message",
						Description: "訊息連結或訊息 ID",
						Required:    true,
					},
				},
			},
		},
	}
}

func (rr *reactionRole) handleInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type != discordgo.InteractionApplicationCommand {
		return
	}
	data := i.ApplicationCommandData()
	if data.Name != reactionRoleCommandName {
		return
	}
	if i.GuildID == "" {
		respondEphemeral(s, i, "這個指令只能在伺服器內使用。", false)
		return
	}
	if !memberCanManageRoles(i.Member) {
		respondEphemeral(s, i, "你沒有管理身分組的權限。", false)
		return
	}
	if len(data.Options) == 0 {
		respondEphemeral(s, i, "缺少子指令。", false)
		return
	}

	switch sub := data.Options[0]; sub.Name {
	case "map-add":
		rr.handleMapAdd(s, i, sub)
	case "map-remove":
		rr.handleMapRemove(s, i, sub)
	case "list":
		rr.handleList(s, i)
	case "bind":
		rr.handleBind(s, i, sub)
	case "unbind":
		rr.handleUnbind(s, i, sub)
	default:
		respondEphemeral(s, i, "未知的子指令。", false)
	}
}

func (rr *reactionRole) handleMapAdd(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	emojiOpt := sub.GetOption("emoji")
	roleOpt := sub.GetOption("role")
	if emojiOpt == nil || roleOpt == nil {
		respondEphemeral(s, i, "缺少 emoji 或 role 參數。", false)
		return
	}

	emojiKey, ok := parseEmojiInput(emojiOpt.StringValue())
	if !ok {
		respondEphemeral(s, i, "表情符號格式無效。", false)
		return
	}
	role := roleOpt.RoleValue(s, i.GuildID)
	if role == nil || role.ID == "" {
		respondEphemeral(s, i, "找不到指定的身分組。", false)
		return
	}

	if err := rr.store.addMapping(i.GuildID, emojiKey, role.ID); err != nil {
		rr.logger.Error("save reaction role mapping", "error", err)
		respondEphemeral(s, i, "儲存設定失敗。", false)
		return
	}

	for messageID, channelID := range rr.store.messagesOf(i.GuildID) {
		if err := s.MessageReactionAdd(channelID, messageID, emojiKey); err != nil {
			rr.logger.Error("seed reaction role emoji", "error", err, "channel_id", channelID, "message_id", messageID, "emoji", emojiKey)
		}
	}

	respondEphemeral(s, i, fmt.Sprintf("已設定 %s -> <@&%s>。", emojiKey, role.ID), true)
}

func (rr *reactionRole) handleMapRemove(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	emojiOpt := sub.GetOption("emoji")
	if emojiOpt == nil {
		respondEphemeral(s, i, "缺少 emoji 參數。", false)
		return
	}

	emojiKey, ok := parseEmojiInput(emojiOpt.StringValue())
	if !ok {
		respondEphemeral(s, i, "表情符號格式無效。", false)
		return
	}

	if err := rr.store.removeMapping(i.GuildID, emojiKey); err != nil {
		rr.logger.Error("remove reaction role mapping", "error", err)
		respondEphemeral(s, i, "儲存設定失敗。", false)
		return
	}

	for messageID, channelID := range rr.store.messagesOf(i.GuildID) {
		if err := s.MessageReactionRemove(channelID, messageID, emojiKey, "@me"); err != nil {
			rr.logger.Error("remove seeded reaction role emoji", "error", err, "channel_id", channelID, "message_id", messageID, "emoji", emojiKey)
		}
	}

	respondEphemeral(s, i, fmt.Sprintf("已移除 %s 的對應。", emojiKey), false)
}

func (rr *reactionRole) handleList(s *discordgo.Session, i *discordgo.InteractionCreate) {
	mappings := rr.store.listMappings(i.GuildID)
	if len(mappings) == 0 {
		respondEphemeral(s, i, "目前沒有任何表情符號身分組對應。", false)
		return
	}

	keys := make([]string, 0, len(mappings))
	for emojiKey := range mappings {
		keys = append(keys, emojiKey)
	}
	sort.Strings(keys)

	lines := make([]string, 0, len(keys))
	for _, emojiKey := range keys {
		lines = append(lines, fmt.Sprintf("%s -> <@&%s>", emojiKey, mappings[emojiKey]))
	}

	respondEphemeral(s, i, strings.Join(lines, "\n"), true)
}

func (rr *reactionRole) handleBind(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	msgOpt := sub.GetOption("message")
	if msgOpt == nil {
		respondEphemeral(s, i, "缺少 message 參數。", false)
		return
	}

	channelID, messageID, ok := parseMessageRef(msgOpt.StringValue(), i.ChannelID)
	if !ok {
		respondEphemeral(s, i, "訊息格式無效，請提供訊息連結或目前頻道內的訊息 ID。", false)
		return
	}
	if _, err := s.ChannelMessage(channelID, messageID); err != nil {
		rr.logger.Error("fetch reaction role panel message", "error", err, "channel_id", channelID, "message_id", messageID)
		respondEphemeral(s, i, "找不到指定訊息，或 bot 沒有讀取該頻道的權限。", false)
		return
	}

	if err := rr.store.bindMessage(i.GuildID, messageID, channelID); err != nil {
		rr.logger.Error("bind reaction role message", "error", err)
		respondEphemeral(s, i, "儲存設定失敗。", false)
		return
	}

	for _, emojiKey := range rr.store.emojiKeys(i.GuildID) {
		if err := s.MessageReactionAdd(channelID, messageID, emojiKey); err != nil {
			rr.logger.Error("seed reaction role emoji", "error", err, "channel_id", channelID, "message_id", messageID, "emoji", emojiKey)
		}
	}

	respondEphemeral(s, i, "已指定該訊息為反應面板。", false)
}

func (rr *reactionRole) handleUnbind(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	msgOpt := sub.GetOption("message")
	if msgOpt == nil {
		respondEphemeral(s, i, "缺少 message 參數。", false)
		return
	}

	_, messageID, ok := parseMessageRef(msgOpt.StringValue(), i.ChannelID)
	if !ok {
		respondEphemeral(s, i, "訊息格式無效，請提供訊息連結或目前頻道內的訊息 ID。", false)
		return
	}

	if err := rr.store.unbindMessage(i.GuildID, messageID); err != nil {
		rr.logger.Error("unbind reaction role message", "error", err)
		respondEphemeral(s, i, "儲存設定失敗。", false)
		return
	}

	respondEphemeral(s, i, "已取消指定該反應面板。", false)
}

func (rr *reactionRole) handleReactionAdd(s *discordgo.Session, e *discordgo.MessageReactionAdd) {
	if e == nil || e.MessageReaction == nil || e.GuildID == "" {
		return
	}
	if isSelfReaction(s, e.UserID) {
		return
	}
	if !rr.store.isDesignated(e.GuildID, e.MessageID) {
		return
	}
	roleID, ok := rr.store.roleForEmoji(e.GuildID, e.Emoji.APIName())
	if !ok {
		return
	}
	if err := s.GuildMemberRoleAdd(e.GuildID, e.UserID, roleID); err != nil {
		rr.logger.Error("grant reaction role", "error", err, "guild_id", e.GuildID, "user_id", e.UserID, "role_id", roleID)
	}
}

func (rr *reactionRole) handleReactionRemove(s *discordgo.Session, e *discordgo.MessageReactionRemove) {
	if e == nil || e.MessageReaction == nil || e.GuildID == "" {
		return
	}
	if isSelfReaction(s, e.UserID) {
		return
	}
	if !rr.store.isDesignated(e.GuildID, e.MessageID) {
		return
	}
	roleID, ok := rr.store.roleForEmoji(e.GuildID, e.Emoji.APIName())
	if !ok {
		return
	}
	if err := s.GuildMemberRoleRemove(e.GuildID, e.UserID, roleID); err != nil {
		rr.logger.Error("revoke reaction role", "error", err, "guild_id", e.GuildID, "user_id", e.UserID, "role_id", roleID)
	}
}

func memberCanManageRoles(member *discordgo.Member) bool {
	if member == nil {
		return false
	}
	required := int64(discordgo.PermissionManageRoles | discordgo.PermissionAdministrator)
	return member.Permissions&required != 0
}

func isSelfReaction(s *discordgo.Session, userID string) bool {
	return s.State != nil && s.State.User != nil && userID == s.State.User.ID
}

func respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, msg string, suppressMentions bool) {
	data := &discordgo.InteractionResponseData{
		Content: msg,
		Flags:   discordgo.MessageFlagsEphemeral,
	}
	if suppressMentions {
		data.AllowedMentions = noMentions()
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: data,
	}); err != nil {
		slog.Default().Error("respond interaction", "error", err)
	}
}

func parseEmojiInput(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if matches := customEmojiPattern.FindStringSubmatch(raw); matches != nil {
		return matches[1] + ":" + matches[2], true
	}
	return raw, true
}

func parseMessageRef(raw, fallbackChannelID string) (channelID, messageID string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	if matches := messageLinkPattern.FindStringSubmatch(raw); matches != nil {
		return matches[1], matches[2], true
	}
	if snowflakePattern.MatchString(raw) && fallbackChannelID != "" {
		return fallbackChannelID, raw, true
	}
	return "", "", false
}
