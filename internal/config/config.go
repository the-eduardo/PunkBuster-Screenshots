// Package config carrega e valida a configuração do bot a partir de variáveis de ambiente.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Server        string
	User          string
	Password      string
	SFTPFolder    string
	BotToken      string
	ChannelID     string
	WaitingTime   time.Duration
	SelectFTPMode string // "ftp" ou "sftp"

	ServerName string // rótulo do servidor de origem, usado na coluna "server" do banco

	DBPath         string
	TempDir        string
	RetentionHours int

	DebugMode bool
}

func Load() (*Config, error) {
	var cfg Config
	var missing []string

	get := func(key string) string { return strings.TrimSpace(os.Getenv(key)) }

	cfg.Server = get("SERVER")
	cfg.User = get("USER")
	cfg.Password = get("PASS")
	cfg.SFTPFolder = get("SFTP_FOLDER")
	cfg.BotToken = get("BOT_TOKEN")
	cfg.ChannelID = get("CHANNEL_ID")

	for _, req := range []struct{ name, val string }{
		{"SERVER", cfg.Server},
		{"USER", cfg.User},
		{"PASS", cfg.Password},
		{"SFTP_FOLDER", cfg.SFTPFolder},
		{"BOT_TOKEN", cfg.BotToken},
		{"CHANNEL_ID", cfg.ChannelID},
	} {
		if req.val == "" {
			missing = append(missing, req.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("variáveis de ambiente obrigatórias não definidas: %s", strings.Join(missing, ", "))
	}

	cfg.SelectFTPMode = strings.ToLower(get("SELECT_FTP_MODE"))
	if cfg.SelectFTPMode != "sftp" && cfg.SelectFTPMode != "ftp" {
		cfg.SelectFTPMode = "ftp"
	}

	waitingMinutes, err := strconv.Atoi(get("WAITING_TIME"))
	if err != nil || waitingMinutes < 2 || waitingMinutes > 120 {
		waitingMinutes = 30
	}
	cfg.WaitingTime = time.Duration(waitingMinutes) * time.Minute

	cfg.ServerName = get("SERVER_NAME")
	if cfg.ServerName == "" {
		cfg.ServerName = cfg.Server
	}

	cfg.DBPath = get("DB_PATH")
	if cfg.DBPath == "" {
		cfg.DBPath = "/data/pbss.db"
	}

	cfg.TempDir = get("TEMP_DIR")
	if cfg.TempDir == "" {
		cfg.TempDir = "/data/tmp"
	}

	cfg.RetentionHours, err = strconv.Atoi(get("RETENTION_HOURS"))
	if err != nil || cfg.RetentionHours <= 0 {
		cfg.RetentionHours = 24
	}

	cfg.DebugMode, _ = strconv.ParseBool(get("DEBUG_MODE"))

	return &cfg, nil
}
