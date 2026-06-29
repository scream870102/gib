# Gib

使用 [bwmarrin/discordgo](https://github.com/bwmarrin/discordgo) 寫的 Discord bot。它會監聽伺服器文字訊息，包含一般文字頻道與語音頻道內建文字聊天，只要整則訊息是一個符合 regex 的 Instagram 連結，就把尾端的追蹤 query 拿掉。

## Discord 限制

Discord API 不允許 bot 修改其他使用者訊息的 `content`。這個專案因此提供四種模式：

- `reply`: 預設模式。bot 回覆原訊息，內容是清理後的連結。
- `delete-repost`: 需要 `Manage Messages` 權限。bot 刪除原訊息，再用 bot 身分發出清理後的連結。
- `webhook-repost`: 需要 `Manage Messages` 和 `Manage Webhooks` 權限。bot 刪除原訊息，再透過 webhook 以原發送者的顯示名稱與頭貼發出清理後的連結。
- `edit-own`: 只會編輯 bot 自己送出的訊息，無法處理其他使用者的訊息。

`webhook-repost` 只能做到視覺上接近原發送者；它不會變成真正由該使用者送出，Discord 仍可能顯示 webhook 或 bot 標記，點開也不會是原使用者帳號。

## 設定

建立 `.env`：

```env
DISCORD_BOT_TOKEN=你的_bot_token
BOT_ACTION=reply
WEBHOOK_NAME=Gib
```

`CLEAN_LINK_REGEX` 不可以省略；  

推薦給ig用的regex如下
```text
(?i)(https?://(?:www\.)?instagram\.com/[^\s<>()?]+/)\?(?:utm_source=[^\s<>()&]+&)?igsh=[^\s<>()&]+
上面這個是沒有放在Json string裡的格式

如果放在Json string裡要用""包裹 且escape \
"(?i)(https?://(?:www\\.)?instagram\\.com/[^\\s<>()?]+/)\\?(?:utm_source=[^\\s<>()&]+&)?igsh=[^\\s<>()&]+"
```

已支援parse以下兩種格式，並保留網址路徑最後的 `/`：

```text
https://www.instagram.com/reel/DZq4Uc-Boi8/?utm_source=ig_web_copy_link&igsh=NTc4MwjQ2YQ==
https://www.instagram.com/reel/DZjZmC1tF_P/?igsh=OGhumU5bTQ=
```

清成：

```text
https://www.instagram.com/reel/DZq4Uc-Boi8/
https://www.instagram.com/reel/DZjZmC1tF_P/
```

如果要自訂 regex，必須讓第 1 個 capture group 代表「清理後要留下的 URL」。單一格式可以用：

```env
CLEAN_LINK_REGEX=(?i)^(https?://(?:www\.)?instagram\.com/[^\s<>()?]+/)\?igsh=[\x21-\x7E]+$
```

多個格式建議用編號 env，程式會依序嘗試：

```env
CLEAN_LINK_REGEX_1=(?i)^(https?://(?:www\.)?instagram\.com/[^\s<>()?]+/)\?utm_source=ig_web_copy_link&igsh=[\x21-\x7E]+$
CLEAN_LINK_REGEX_2=(?i)^(https?://(?:www\.)?instagram\.com/[^\s<>()?]+/)\?igsh=[\x21-\x7E]+$
```

`CLEAN_LINK_REGEX` 也可以填 JSON array；這種寫法需要把 regex 裡的 `\` 寫成 `\\`。

## Bot 權限

在 Discord Developer Portal 裡：

1. 開啟 `MESSAGE CONTENT INTENT`，否則 bot 讀不到一般訊息內容。
2. 邀請 bot 時選 `bot` 和 `applications.commands` scope。
3. 基本權限至少給 `View Channel`、`Read Message History`、`Send Messages`。
4. 若使用 `BOT_ACTION=delete-repost`，再加 `Manage Messages`。
5. 若使用 `BOT_ACTION=webhook-repost`，再加 `Manage Messages` 和 `Manage Webhooks`。
6. 若語音頻道文字聊天收不到訊息，確認該語音頻道也讓 bot 看得到頻道；必要時給 `Connect`。如果 Discord 不允許在該頻道建立 webhook，log 會顯示 webhook 建立失敗，這時可退回 `delete-repost`。

## 表情符號身分組

此功能使用 slash 指令 `/reactionrole`。只有具備 `Manage Roles` 權限的成員能使用，回覆都是 ephemeral，只有執行者看得到。

```text
/reactionrole map-add emoji:<表情> role:<身分組>
/reactionrole map-remove emoji:<表情>
/reactionrole list
/reactionrole bind message:<訊息連結或ID>
/reactionrole unbind message:<訊息連結或ID>
```

每個伺服器共用一張 `emoji -> role` 對應表。`bind` 指定的訊息會成為反應面板，使用者加上表內表情符號時會取得對應身分組，移除反應時會移除身分組。`map-add` 會把新表情補貼到所有已指定面板，`map-remove` 會移除 bot 自己在面板上的種子反應。

bot 需要 `Manage Roles`、`Add Reactions`、`View Channel`、`Read Message History`。Discord 角色階層也必須正確：bot 的最高身分組要高於所有要發放的身分組，否則 Discord 會回 403。這個功能不需要開啟 `SERVER MEMBERS INTENT`。

設定會寫到 `REACTION_ROLE_STATE_DIR` 底下的 guild 分片 JSON，Docker 預設建議用 `/data/reactionroles/<guild_id>.json`，並掛載 `/data` volume 才能在重啟後保留資料。舊的 `REACTION_ROLE_STATE_FILE` 只作為一次性遷移來源：如果分片資料夾還沒有任何 `*.json`，啟動時會把舊 `/data/reactionroles.json` 拆成多個 guild 檔案，之後不再寫回舊檔。`COMMAND_GUILD_ID` 可填測試伺服器 ID，讓 slash 指令即時註冊；留空時會註冊全域指令，Discord 最久可能約 1 小時才更新。
可選擇設定 `REACTION_ROLE_REMOTE_DATABASE_URL` 指向 Neon/Postgres。啟動時會自動建立 `reaction_role_guild_state` table，逐 guild 比較本地與遠端的 `updated_at`，用較新的資料補齊或覆蓋較舊的一邊。運行中 slash 指令只會把被修改的 guild 標記為 dirty，背景同步依 `REACTION_ROLE_SYNC_INTERVAL` 推送，預設 `15m`。遠端連不上只會記 log，bot 仍使用本地資料繼續運作，dirty guild 會留到下一次同步重試。

## 本機執行

```powershell
go mod tidy
go test ./...
go run .
```

## Synology NAS Container Manager 部署

1. 把專案放到 NAS 上，例如 `/volume1/docker/gib`。
2. 在該資料夾建立 `.env`，填入 `DISCORD_BOT_TOKEN`。
3. 打開 Container Manager，使用 Project 匯入 `compose.yaml`。
4. 啟動專案後看 log，應該會看到 `bot is ready`。

如果你用 SSH，也可以在專案資料夾執行：

```sh
docker compose up -d --build
docker compose logs -f gib
```

