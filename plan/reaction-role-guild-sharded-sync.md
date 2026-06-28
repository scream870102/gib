---
title: Reaction Role Guild 分片同步方案
tags:
  - gib
  - discord-bot
  - reaction-role
  - sync
  - neon
  - plan
status: planned
---

# Reaction Role Guild 分片同步方案

> [!summary]
> Reaction role 設定改成每個 guild 一個 JSON。本地仍保留完整副本，遠端 Neon Postgres 也保存每個 guild 的 JSON 副本。啟動時逐 guild 比對 `updated_at`，運行中每 15 分鐘只同步有變更的 guild。

## 目標行為

- 本地資料從單一 `/data/reactionroles.json` 改成資料夾分片，例如 `/data/reactionroles/<guild_id>.json`。
- 遠端 Neon 也以 guild 為粒度保存資料，一個 guild 對應一列。
- Bot 啟動時：
  - 先載入本地所有 guild JSON。
  - 若有 Neon 連線字串，讀取遠端所有 guild state。
  - 對本地與遠端 guild ID 取聯集，逐 guild 比較 `updated_at`。
  - 使用較新的資料覆蓋較舊的一邊。
- Bot 運行中：
  - slash 指令修改某個 guild 後，只把該 guild 標記為 dirty。
  - 每 15 分鐘只把 dirty guild 推送到 Neon。
  - 同步成功後清除 dirty 標記。
- Neon 連不上時：
  - 記錄 log。
  - Bot 仍使用本地資料照常啟動與運作。
  - dirty guild 保留到下一次同步重試。

## 資料格式

### 本地 JSON

路徑：

```text
/data/reactionroles/<guild_id>.json
```

格式：

```json
{
  "guild_id": "guild-id",
  "updated_at": "2026-06-28T10:30:00Z",
  "mappings": {
    "😀": "role-id",
    "party:123": "role-id-2"
  },
  "messages": {
    "message-id": "channel-id"
  }
}
```

### Neon Table

```sql
create table if not exists reaction_role_guild_state (
  guild_id text primary key,
  updated_at timestamptz not null,
  data jsonb not null
);
```

- `guild_id`：Discord guild ID。
- `updated_at`：該 guild state 的最後更新時間。
- `data`：只存該 guild 的完整 JSON state。

## 實作重點

### 設定

新增設定：

```env
REACTION_ROLE_STATE_DIR=/data/reactionroles
REACTION_ROLE_REMOTE_DATABASE_URL=postgres://...
REACTION_ROLE_SYNC_INTERVAL=15m
```

保留舊設定：

```env
REACTION_ROLE_STATE_FILE=/data/reactionroles.json
```

`REACTION_ROLE_STATE_FILE` 只作為舊資料的一次性遷移來源。若 `REACTION_ROLE_STATE_DIR` 還沒有分片資料，但舊檔存在，就把舊檔拆成多個 guild JSON；遷移後不再寫入舊單一 JSON。

### Store 分片

- 啟動時讀取 `REACTION_ROLE_STATE_DIR` 底下所有 `*.json`。
- 記憶體中仍維持 `guildID -> guildConfig` 的 map，讓 reaction event 查詢不需要碰磁碟或遠端 DB。
- `addMapping`、`removeMapping`、`bindMessage`、`unbindMessage` 只更新該 guild：
  - 更新該 guild 的 `updated_at`。
  - 只寫回該 guild 的 JSON 檔。
  - 標記該 guild 為 dirty。
- `listMappings`、`messagesOf` 仍回傳 copy，避免外部修改污染 store。

### 啟動同步

- Neon URL 空值時，完全使用純本地模式。
- Neon 可用時，自動建立 table。
- 遠端不存在某 guild，本地存在：推送本地 guild 到遠端。
- 本地不存在某 guild，遠端存在：寫成新的本地 guild JSON。
- 兩邊都存在：
  - 遠端 `updated_at` 較新：覆蓋本地該 guild。
  - 本地 `updated_at` 較新：推送本地該 guild。
  - 時間相同：不處理。
- Neon 連線或查詢失敗時，不讓 bot 啟動失敗。

### 定時同步

- 背景 goroutine 使用 `time.Ticker`，間隔預設 15 分鐘。
- 每次 tick 只同步 dirty guild。
- 某個 guild 同步成功後，只清除該 guild 的 dirty 標記。
- 同步失敗時保留 dirty 標記，等待下一次重試。

### 刪除策略

第一版不做 guild JSON 檔刪除同步。

- `map-remove` 只刪除 mapping。
- `unbind` 只刪除 message binding。
- 即使某 guild 的 `mappings` 和 `messages` 都空了，也保留本地 JSON 和遠端 row。

這樣可以避免誤刪造成資料遺失。

## 測試項目

- [ ] `go test ./...`
- [ ] 對 `guild-1` 修改只建立或更新 `guild-1.json`。
- [ ] 多個 guild 載入後，查詢結果互不影響。
- [ ] `listMappings` 和 `messagesOf` 回傳 copy，外部 mutation 不會污染 store。
- [ ] 舊版 `reactionroles.json` 可拆成多個 `<guild_id>.json`。
- [ ] 已有分片資料時，不重複遷移舊單一 JSON。
- [ ] 遠端某 guild 較新時，只覆蓋該 guild 的本地檔。
- [ ] 本地某 guild 較新時，只推送該 guild 到遠端。
- [ ] 本地與遠端各有不同 guild 時，會補齊缺少的一邊。
- [ ] Neon 連線失敗時，仍可用本地資料啟動。
- [ ] 定時同步只推 dirty guild。
- [ ] 同步成功會清 dirty，同步失敗會保留 dirty。

## 假設

- 遠端仍用 JSONB，但粒度改成每個 guild 一列。
- 啟動時比較粒度是每個 guild，不是整個 bot 狀態。
- 運行中只定時把本地 dirty guild 推到遠端，不定時從遠端拉回。
- 本地資料是主要操作來源；遠端是備份與跨啟動還原來源。
- 第一版不做逐筆 merge。
- 第一版不做刪除同步。
