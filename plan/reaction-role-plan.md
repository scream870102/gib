# 新增功能：表情符號身分組（Reaction Roles）

## Context（背景）

目前 `gib` 是一個單一功能的 Go Discord bot（`bwmarrin/discordgo` v0.29.0）：監聽訊息、清理 Instagram 連結。架構是「每個功能一個 `xFeatureRegister(session, cfg, logger)`」，設定全部來自環境變數，部署是無狀態的 Docker 容器（`compose.yaml` 目前沒有任何 volume）。

使用者想新增「表情符號身分組」功能：

- 每個伺服器有 **一張共用的 `emoji → 身分組` 對應表**。
- 管理者可以把某則訊息「指定」為反應面板；在該訊息下按下表內表情符號的人會自動拿到對應身分組，移除反應就移除身分組。
- 只有具備 **Manage Roles 權限** 的成員可以新增/移除/查看這張表與指定訊息；其他人的指令一律拒絕。
- 設定需在 bot 重啟後不遺失，因此寫入掛載的 **JSON 檔（Docker volume）**。

已與使用者確認的四個決策：Slash 指令介面、Manage Roles 權限判定、JSON 檔＋Docker volume 持久化、全伺服器共用一張表。

本功能與現有連結清理功能**並存**，不更動連結功能行為。

## 行為總覽

Slash 指令（全部用 `DefaultMemberPermissions = ManageRoles` 原生限制，並在 server 端再驗證一次）：

| 指令 | 動作 |
|------|------|
| `/reactionrole map-add emoji:<表情> role:<身分組>` | 在伺服器表加入一筆對應，並把該表情補貼到所有已指定的訊息 |
| `/reactionrole map-remove emoji:<表情>` | 移除一筆對應，並把 bot 在已指定訊息上的該表情反應撤掉 |
| `/reactionrole list` | 以 ephemeral（只有自己看得到）列出整張表 |
| `/reactionrole bind message:<訊息連結或ID>` | 指定一則訊息為反應面板，bot 自動貼上表內所有表情 |
| `/reactionrole unbind message:<訊息連結或ID>` | 取消指定 |

執行階段：使用者在「已指定訊息」下加/移反應 → 查表 → `GuildMemberRoleAdd` / `GuildMemberRoleRemove`。bot 自己貼的種子反應與自己的 UserID 都會被忽略。

## 檔案異動清單

新增：
- `store.go` — JSON 持久化 + 每伺服器設定的存取方法。
- `reactionrole.go` — 指令定義、interaction handler、reaction handlers、表情/訊息字串解析。
- `store_test.go`、`reactionrole_test.go` — 純邏輯單元測試（不需連 Discord）。

修改：
- `main.go` — 加入 `IntentsGuildMessageReactions`、註冊新功能。
- `config.go` — 讀取狀態檔路徑與（選用的）即時註冊用 guild id。
- `Dockerfile` — 建立可寫的 `/data` 目錄並 `chown` 給 `app` 使用者。
- `compose.yaml` — 加入 named volume 掛載到 `/data`。
- `.env.example`、`.gitignore`、`README.md` — 文件與本機忽略項。

## 詳細實作

### 1. `store.go` — 持久化

```go
type guildConfig struct {
    Mappings map[string]string `json:"mappings"` // emojiKey -> roleID
    Messages map[string]string `json:"messages"` // messageID -> channelID（已指定的面板）
}

type stateData struct {
    Guilds map[string]*guildConfig `json:"guilds"` // guildID -> config
}

type store struct {
    mu   sync.RWMutex
    path string
    data stateData
}
```

- `loadStore(path string) (*store, error)`：`os.MkdirAll(filepath.Dir(path), 0o755)`；檔案存在就 `json.Unmarshal`，不存在就用空的 `stateData`（不算錯誤）。
- `(*s) save() error`：在持鎖狀態下 `os.WriteFile(path, data, 0o644)`（寫入量小、併發低，直接覆寫即可；跨平台比 rename 安全）。
- 變更型方法都先 `Lock`、改記憶體、`save()`、`Unlock`，並在沒有該 guild 時自動建立：`addMapping/removeMapping/listMappings/bindMessage/unbindMessage`。
- 查詢型用 `RLock`：`roleForEmoji(guildID, key) (string, bool)`、`isDesignated(guildID, messageID) bool`、`messagesOf(guildID) map[string]string`、`emojiKeys(guildID) []string`。

### 2. `reactionrole.go` — 主功能

**註冊函式**（沿用既有模式）：

```go
func reactionRoleFeatureRegister(s *discordgo.Session, cfg reactionRoleConfig, logger *slog.Logger) error
```

- 建立 `reactionRole` struct，持有 `*store`、`logger`、`commandGuildID`。
- `s.AddHandler(rr.handleReactionAdd)`、`s.AddHandler(rr.handleReactionRemove)`、`s.AddHandler(rr.handleInteraction)`。
- `s.AddHandler(func(s, r *discordgo.Ready){ rr.registerCommands(s) })`：在 ready 後用 `s.ApplicationCommandCreate(s.State.User.ID, cfg.CommandGuildID, cmd)` 註冊。`CommandGuildID` 為空字串時是**全域指令**（最久約 1 小時生效）；填某個測試伺服器 ID 則**即時生效**，方便開發。

**指令定義**：單一 top-level `reactionrole`，內含 5 個 SubCommand。
```go
manage := int64(discordgo.PermissionManageRoles)
dmFalse := false
cmd := &discordgo.ApplicationCommand{
    Name: "reactionrole", Description: "管理表情符號身分組",
    DefaultMemberPermissions: &manage, DMPermission: &dmFalse,
    Options: []*discordgo.ApplicationCommandOption{ /* map-add, map-remove, list, bind, unbind */ },
}
```
- `map-add`：`emoji`（String，required）、`role`（`OptionRole`，required）。
- `map-remove` / `bind` / `unbind`：各一個 String required 參數。
- `list`：無參數。

**`handleInteraction`**：
- 只處理 `InteractionApplicationCommand` 且 `data.Name == "reactionrole"`；其餘 return。
- 若 `i.GuildID == ""` → ephemeral 提示「只能在伺服器內使用」。
- **Server 端權限再驗證**（雙保險）：`i.Member.Permissions & (PermissionManageRoles|PermissionAdministrator) != 0`，否則 ephemeral 回「你沒有權限」。
- 取出 `sub := data.Options[0]`，`switch sub.Name`：
  - **map-add**：`parseEmojiInput(sub.GetOption("emoji").StringValue())` 取得 key；`sub.GetOption("role").RoleValue(s, i.GuildID)` 取得 `*Role`。`store.addMapping`。接著對 `store.messagesOf(guild)` 每則訊息 `s.MessageReactionAdd(channelID, msgID, key)`（補貼新表情，失敗只記 log）。ephemeral 回確認。
  - **map-remove**：`store.removeMapping`；對每則已指定訊息 `s.MessageReactionRemove(channelID, msgID, key, "@me")` 撤掉 bot 自己的種子反應。ephemeral 回確認。
  - **list**：組出 `表情 → <@&roleID>` 多行字串，ephemeral 回覆，`AllowedMentions` 設為不 tag（沿用 `noMentions()`）。空表給友善提示。
  - **bind**：`parseMessageRef(raw, i.ChannelID)` 解析出 `channelID, messageID`；用 `s.ChannelMessage(channelID, messageID)` 確認訊息存在（失敗回錯誤）。`store.bindMessage`。對 `store.emojiKeys(guild)` 逐一 `s.MessageReactionAdd` 貼上所有表內表情。ephemeral 回確認。
  - **unbind**：`store.unbindMessage`，ephemeral 回確認。

回覆統一用小工具：
```go
func respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
    s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
        Type: discordgo.InteractionResponseChannelMessageWithSource,
        Data: &discordgo.InteractionResponseData{Content: msg, Flags: discordgo.MessageFlagsEphemeral},
    })
}
```

**Reaction handlers**：
```go
func (rr *reactionRole) handleReactionAdd(s *discordgo.Session, e *discordgo.MessageReactionAdd) {
    if e.GuildID == "" { return }
    if s.State != nil && s.State.User != nil && e.UserID == s.State.User.ID { return } // 忽略 bot 自己
    if !rr.store.isDesignated(e.GuildID, e.MessageID) { return }
    roleID, ok := rr.store.roleForEmoji(e.GuildID, e.Emoji.APIName())
    if !ok { return }
    if err := s.GuildMemberRoleAdd(e.GuildID, e.UserID, roleID); err != nil {
        rr.logger.Error("grant reaction role", "error", err) // 多半是缺 Manage Roles 或角色階層
    }
}
```
`handleReactionRemove` 同樣邏輯，改呼叫 `s.GuildMemberRoleRemove`。**emoji key 一律用 `e.Emoji.APIName()`**，與指令端 `parseEmojiInput` 產生的 key 一致。`MessageReactionRemoveAll` / `RemoveEmoji` v1 先忽略（無法得知是誰、文件註明）。

**解析小工具（純函式，好測試）**：
- `parseEmojiInput(raw string) (key string, ok bool)`：
  - 自訂表情輸入會是字面 `<:name:id>` 或 `<a:name:id>` → 用 `regexp` 取出 → key = `name:id`（對齊 `APIName()`）。
  - 否則視為 unicode 表情 → key = `strings.TrimSpace(raw)`。
  - key 為空則 `ok=false`。
- `parseMessageRef(raw, fallbackChannelID string) (channelID, messageID string, ok bool)`：
  - 比對訊息連結 `channels/(\d+)/(\d+)/(\d+)` → channel=group2、message=group3。
  - 否則若 raw 全為數字 → channel=`fallbackChannelID`、message=raw。
  - 其餘 `ok=false`。

### 3. `main.go`

- intents 加入反應事件：
  ```go
  session.Identify.Intents = discordgo.IntentsGuilds |
      discordgo.IntentsGuildMessages |
      discordgo.IntentsMessageContent |
      discordgo.IntentsGuildMessageReactions
  ```
- 在連結功能註冊之後加入：
  ```go
  if err := reactionRoleFeatureRegister(session, cfg.reactionRole, logger); err != nil {
      logger.Error("register reaction roles", "error", err); os.Exit(1)
  }
  ```

### 4. `config.go`

- `config` 加上 `reactionRole reactionRoleConfig`。
- 新增 `reactionRoleConfig{ StateFile string; CommandGuildID string }`。
- `loadConfig` 內：
  - `StateFile = envOrDefault("REACTION_ROLE_STATE_FILE", "data/reactionroles.json")`（容器內預設值由 `.env`/compose 覆寫成 `/data/reactionroles.json`）。
  - `CommandGuildID = strings.TrimSpace(os.Getenv("COMMAND_GUILD_ID"))`（可空）。
- 不更動既有 `CLEAN_LINK_REGEX` 必填邏輯。

### 5. Docker / compose / env / gitignore

- `Dockerfile`：在建立 `app` 使用者後加上
  ```dockerfile
  RUN mkdir -p /data && chown app /data
  VOLUME ["/data"]
  ```
- `compose.yaml`：
  ```yaml
      volumes:
        - gib-data:/data
  volumes:
    gib-data:
  ```
- `.env.example` 增加：
  ```env
  REACTION_ROLE_STATE_FILE=/data/reactionroles.json
  COMMAND_GUILD_ID=
  ```
- `.gitignore` 增加本機資料目錄：`data/`。

### 6. `README.md`

新增一節「表情符號身分組」，說明：
- 5 個 slash 指令用法與「只有 Manage Roles 權限可用、回覆只有自己看得到」。
- bot 權限需求：**Manage Roles**、Add Reactions、View Channel、Read Message History，且 **bot 的最高身分組必須高於要發放的每個身分組**（Discord 角色階層限制，否則 403）。
- 邀請 bot 時要同時勾選 `bot` 與 `applications.commands` scope。
- 需開啟 `SERVER MEMBERS`？不需要——授予/移除身分組與反應事件不需 Members intent；只需新增的 `IntentsGuildMessageReactions`（程式內設定，非 Portal）。
- 部署需掛載 `/data` volume 才能保存設定；`COMMAND_GUILD_ID` 可填測試伺服器 ID 讓指令即時生效。

### 7. 測試（`*_test.go`）

- `reactionrole_test.go`：表格測試 `parseEmojiInput`（unicode、`<:name:id>`、`<a:name:id>`、空字串）與 `parseMessageRef`（完整連結、純 ID、亂填）。
- `store_test.go`：用 `t.TempDir()` 建臨時檔，測 add/remove/list、bind/unbind、`roleForEmoji`、`isDesignated`，以及 save→重新 `loadStore` 後資料一致（round-trip）。

## 權限與邀請須知（執行前確認）

1. Discord Developer Portal：bot 邀請連結需含 `applications.commands` scope（否則 slash 指令不會出現）。
2. bot 需 **Manage Roles** 權限，且其最高身分組要排在所有受管身分組**之上**。
3. 部署務必掛 `/data` volume，否則重啟後表會遺失。

## 驗證方式

1. **單元測試**：`go test ./...`（解析與 store round-trip 應全綠）。
2. **編譯**：`go build .` 應通過。
3. **本機實機**（`.env` 填 token，建議設 `COMMAND_GUILD_ID` 讓指令即時出現）：`go run .`，log 出現 `bot is ready`。
4. 在測試伺服器：
   - `/reactionrole map-add emoji:😀 role:@SomeRole` → 應回 ephemeral 確認；`/reactionrole list` 看到該筆。
   - 發一則訊息，`/reactionrole bind message:<該訊息連結>` → bot 自動貼上 😀。
   - 用另一個帳號點 😀 → 取得身分組；移除 😀 → 失去身分組。
   - 用沒有 Manage Roles 的帳號執行指令 → 指令被原生隱藏／被拒。
   - `/reactionrole map-remove emoji:😀` → bot 種子反應消失。
5. **持久化**：重啟 bot（或 `docker compose restart`）後 `/reactionrole list` 仍保有資料、原面板反應仍能發放身分組。
