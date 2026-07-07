CREATE TABLE IF NOT EXISTS players (
    guid TEXT PRIMARY KEY,
    first_seen DATETIME NOT NULL,
    last_seen DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS player_names (
    guid TEXT NOT NULL REFERENCES players(guid),
    name TEXT NOT NULL,
    last_seen DATETIME NOT NULL,
    PRIMARY KEY (guid, name)
);

CREATE TABLE IF NOT EXISTS screenshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    guid TEXT NOT NULL REFERENCES players(guid),
    player_name TEXT NOT NULL,
    filename TEXT NOT NULL,
    captured_at DATETIME,
    received_at DATETIME NOT NULL,
    server TEXT NOT NULL,
    discord_guild_id TEXT,
    discord_channel_id TEXT,
    discord_message_id TEXT
);

CREATE INDEX IF NOT EXISTS idx_screenshots_guid ON screenshots(guid);
CREATE INDEX IF NOT EXISTS idx_screenshots_name ON screenshots(player_name);
CREATE INDEX IF NOT EXISTS idx_player_names_name ON player_names(name);
