package storage

import (
	"database/sql"
	"time"
)

// ScreenshotRecord é uma linha da tabela screenshots já enriquecida pro uso do dashboard.
type ScreenshotRecord struct {
	ID                int64
	GUID              string
	PlayerName        string
	FileName          string
	CapturedAt        time.Time
	ReceivedAt        time.Time
	Server            string
	DiscordGuildID    string
	DiscordChannelID  string
	DiscordMessageID  string
}

// RecordScreenshot registra um screenshot já confirmado como enviado ao Discord.
// Deve ser chamado somente após a confirmação de envio (nunca antes) para que o
// índice reflita fielmente o que está de fato arquivado no canal.
func (s *Store) RecordScreenshot(rec ScreenshotRecord) error {
	now := rec.ReceivedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`
		INSERT INTO players (guid, first_seen, last_seen) VALUES (?, ?, ?)
		ON CONFLICT(guid) DO UPDATE SET last_seen = excluded.last_seen
	`, rec.GUID, now, now)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`
		INSERT INTO player_names (guid, name, last_seen) VALUES (?, ?, ?)
		ON CONFLICT(guid, name) DO UPDATE SET last_seen = excluded.last_seen
	`, rec.GUID, rec.PlayerName, now)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`
		INSERT INTO screenshots (guid, player_name, filename, captured_at, received_at, server, discord_guild_id, discord_channel_id, discord_message_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, rec.GUID, rec.PlayerName, rec.FileName, rec.CapturedAt, now, rec.Server, rec.DiscordGuildID, rec.DiscordChannelID, rec.DiscordMessageID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// SearchByGUID retorna os screenshots mais recentes de um GUID exato.
func (s *Store) SearchByGUID(guid string, limit int) ([]ScreenshotRecord, error) {
	return s.query(`
		SELECT id, guid, player_name, filename, captured_at, received_at, server, discord_guild_id, discord_channel_id, discord_message_id
		FROM screenshots WHERE guid = ? ORDER BY received_at DESC LIMIT ?
	`, guid, limit)
}

// SearchByName retorna os screenshots mais recentes de jogadores cujo nome contém `name`.
func (s *Store) SearchByName(name string, limit int) ([]ScreenshotRecord, error) {
	return s.query(`
		SELECT id, guid, player_name, filename, captured_at, received_at, server, discord_guild_id, discord_channel_id, discord_message_id
		FROM screenshots WHERE player_name LIKE '%' || ? || '%' ORDER BY received_at DESC LIMIT ?
	`, name, limit)
}

func (s *Store) query(q string, args ...any) ([]ScreenshotRecord, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ScreenshotRecord
	for rows.Next() {
		var rec ScreenshotRecord
		var capturedAt sql.NullTime
		var guildID, channelID, messageID sql.NullString
		if err := rows.Scan(&rec.ID, &rec.GUID, &rec.PlayerName, &rec.FileName, &capturedAt, &rec.ReceivedAt, &rec.Server, &guildID, &channelID, &messageID); err != nil {
			return nil, err
		}
		rec.CapturedAt = capturedAt.Time
		rec.DiscordGuildID = guildID.String
		rec.DiscordChannelID = channelID.String
		rec.DiscordMessageID = messageID.String
		out = append(out, rec)
	}
	return out, rows.Err()
}

// Stats resume os totais usados no /pbss stats.
type Stats struct {
	TotalScreenshots int64
	TotalPlayers     int64
	TopPlayers       []TopPlayer
}

type TopPlayer struct {
	GUID  string
	Name  string
	Count int64
}

func (s *Store) GetStats() (Stats, error) {
	var stats Stats
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM screenshots`).Scan(&stats.TotalScreenshots); err != nil {
		return stats, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM players`).Scan(&stats.TotalPlayers); err != nil {
		return stats, err
	}

	rows, err := s.db.Query(`
		SELECT s.guid, pn.name, COUNT(*) as c
		FROM screenshots s
		JOIN player_names pn ON pn.guid = s.guid AND pn.last_seen = (
			SELECT MAX(last_seen) FROM player_names WHERE guid = s.guid
		)
		GROUP BY s.guid
		ORDER BY c DESC
		LIMIT 10
	`)
	if err != nil {
		return stats, err
	}
	defer rows.Close()
	for rows.Next() {
		var tp TopPlayer
		if err := rows.Scan(&tp.GUID, &tp.Name, &tp.Count); err != nil {
			return stats, err
		}
		stats.TopPlayers = append(stats.TopPlayers, tp)
	}
	return stats, rows.Err()
}
