package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	_ "github.com/lib/pq"
)

const reactionRoleTableDDL = `create table if not exists reaction_role_guild_state (
  guild_id text primary key,
  updated_at timestamptz not null,
  data jsonb not null
)`

type reactionRoleRemote interface {
	Close() error
	EnsureSchema(ctx context.Context) error
	ListGuildStates(ctx context.Context) (map[string]*guildConfig, error)
	PutGuildState(ctx context.Context, cfg *guildConfig) error
}

type postgresReactionRoleRemote struct {
	db *sql.DB
}

func newPostgresReactionRoleRemote(ctx context.Context, url string) (*postgresReactionRoleRemote, error) {
	db, err := sql.Open("postgres", url)
	if err != nil {
		return nil, err
	}
	remote := &postgresReactionRoleRemote{db: db}
	if err := db.PingContext(ctx); err != nil {
		_ = remote.Close()
		return nil, err
	}
	if err := remote.EnsureSchema(ctx); err != nil {
		_ = remote.Close()
		return nil, err
	}
	return remote, nil
}

func (r *postgresReactionRoleRemote) Close() error {
	return r.db.Close()
}

func (r *postgresReactionRoleRemote) EnsureSchema(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, reactionRoleTableDDL)
	return err
}

func (r *postgresReactionRoleRemote) ListGuildStates(ctx context.Context) (map[string]*guildConfig, error) {
	rows, err := r.db.QueryContext(ctx, `select guild_id, updated_at, data from reaction_role_guild_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := map[string]*guildConfig{}
	for rows.Next() {
		var guildID string
		var updatedAt time.Time
		var raw []byte
		if err := rows.Scan(&guildID, &updatedAt, &raw); err != nil {
			return nil, err
		}
		var cfg guildConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("read remote reaction role state for guild %s: %w", guildID, err)
		}
		if cfg.GuildID == "" {
			cfg.GuildID = guildID
		}
		cfg.UpdatedAt = updatedAt.UTC()
		ensureGuildConfig(&cfg)
		result[guildID] = &cfg
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *postgresReactionRoleRemote) PutGuildState(ctx context.Context, cfg *guildConfig) error {
	clone := cloneGuildConfig(cfg)
	payload, err := json.Marshal(clone)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `insert into reaction_role_guild_state (guild_id, updated_at, data)
values ($1, $2, $3::jsonb)
on conflict (guild_id) do update set updated_at = excluded.updated_at, data = excluded.data`, clone.GuildID, clone.UpdatedAt, payload)
	return err
}

type reactionRoleSyncer struct {
	store       *store
	remoteURL   string
	interval    time.Duration
	logger      *slog.Logger
	startupDone bool
}

func startReactionRoleSyncer(st *store, cfg reactionRoleConfig, logger *slog.Logger) {
	if cfg.RemoteDatabaseURL == "" {
		return
	}

	syncer := &reactionRoleSyncer{
		store:     st,
		remoteURL: cfg.RemoteDatabaseURL,
		interval:  cfg.SyncInterval,
		logger:    logger,
	}
	if syncer.interval <= 0 {
		syncer.interval = 15 * time.Minute
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := syncer.syncOnce(ctx); err != nil {
		logger.Error("sync reaction role state", "error", err)
	}
	cancel()

	go syncer.run(context.Background())
}

func (s *reactionRoleSyncer) run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			attemptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := s.syncOnce(attemptCtx); err != nil {
				s.logger.Error("sync reaction role state", "error", err)
			}
			cancel()
		}
	}
}

func (s *reactionRoleSyncer) syncOnce(ctx context.Context) error {
	remote, err := newPostgresReactionRoleRemote(ctx, s.remoteURL)
	if err != nil {
		return err
	}
	defer func() {
		if err := remote.Close(); err != nil {
			s.logger.Error("close reaction role remote", "error", err)
		}
	}()

	if !s.startupDone {
		if err := s.syncStartup(ctx, remote); err != nil {
			return err
		}
		s.startupDone = true
	}
	return s.syncDirty(ctx, remote)
}

func (s *reactionRoleSyncer) syncStartup(ctx context.Context, remote reactionRoleRemote) error {
	remoteGuilds, err := remote.ListGuildStates(ctx)
	if err != nil {
		return err
	}

	seen := map[string]struct{}{}
	for _, guildID := range s.store.guildIDs() {
		seen[guildID] = struct{}{}
	}
	for guildID := range remoteGuilds {
		seen[guildID] = struct{}{}
	}

	guildIDs := make([]string, 0, len(seen))
	for guildID := range seen {
		guildIDs = append(guildIDs, guildID)
	}
	sort.Strings(guildIDs)

	for _, guildID := range guildIDs {
		local, hasLocal := s.store.guildSnapshot(guildID)
		remoteCfg, hasRemote := remoteGuilds[guildID]
		switch {
		case hasLocal && !hasRemote:
			if err := remote.PutGuildState(ctx, local); err != nil {
				return err
			}
		case !hasLocal && hasRemote:
			if err := s.store.replaceGuild(remoteCfg); err != nil {
				return err
			}
		case hasLocal && hasRemote:
			if remoteCfg.UpdatedAt.After(local.UpdatedAt) {
				if err := s.store.replaceGuild(remoteCfg); err != nil {
					return err
				}
			} else if local.UpdatedAt.After(remoteCfg.UpdatedAt) {
				if err := remote.PutGuildState(ctx, local); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *reactionRoleSyncer) syncDirty(ctx context.Context, remote reactionRoleRemote) error {
	for _, cfg := range s.store.dirtyGuilds() {
		if err := remote.PutGuildState(ctx, cfg); err != nil {
			return err
		}
		s.store.clearDirtyIfUnchanged(cfg.GuildID, cfg.UpdatedAt)
	}
	return nil
}
