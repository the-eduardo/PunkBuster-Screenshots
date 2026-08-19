// Command bot é o ponto de entrada do duck-pbss: conecta no Discord uma única
// vez, sobe o pipeline de polling do SFTP/FTP e os slash commands do
// mini-dashboard, e desliga de forma limpa em SIGTERM/SIGINT.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pbss/internal/config"
	"pbss/internal/discord"
	"pbss/internal/discord/commands"
	"pbss/internal/queue"
	"pbss/internal/source"
	"pbss/internal/storage"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuração inválida", "erro", err)
		os.Exit(1)
	}

	level := slog.LevelInfo
	if cfg.DebugMode {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	store, err := storage.Open(cfg.DBPath)
	if err != nil {
		slog.Error("falha ao abrir banco de dados", "erro", err)
		os.Exit(1)
	}
	defer store.Close()

	session, err := discord.Open(cfg.BotToken)
	if err != nil {
		if errors.Is(err, discord.ErrInvalidToken) {
			slog.Error("token do bot inválido/revogado — gere um token novo no Developer Portal e atualize BOT_TOKEN", "erro", err)
		} else {
			slog.Error("falha ao conectar ao discord", "erro", err)
		}
		os.Exit(1)
	}
	defer session.Close()

	handler := commands.NewHandler(store)
	guildID := os.Getenv("DISCORD_GUILD_ID") // opcional: propaga slash commands na hora nesse servidor, em vez de até 1h globalmente
	if err := handler.Register(session, guildID); err != nil {
		slog.Error("falha ao registrar slash commands", "erro", err)
		os.Exit(1)
	}
	session.AddHandler(handler.HandleInteraction)

	var src source.Source
	if cfg.SelectFTPMode == "sftp" {
		sftpSrc, err := source.NewSFTPSource(cfg.Server, cfg.User, cfg.Password, cfg.SFTPHostKey, cfg.SFTPInsecureHostKey)
		if err != nil {
			slog.Error("falha ao configurar a origem sFTP", "erro", err)
			os.Exit(1)
		}
		if cfg.SFTPInsecureHostKey && cfg.SFTPHostKey == "" {
			slog.Warn("verificacao da chave do host sFTP DESLIGADA por SFTP_INSECURE_HOST_KEY - a senha trafega para qualquer servidor que atenda no endereco")
		}
		src = sftpSrc
	} else {
		src = source.NewFTPSource(cfg.Server, cfg.User, cfg.Password)
	}
	defer src.Close()

	sender := discord.NewSender(session, 500)
	go sender.Run()
	defer sender.Close(8 * time.Second) // cabe no grace de shutdown de 10s do Docker

	pipeline := &queue.Pipeline{
		ServerLabel:    cfg.ServerName,
		SFTPFolder:     cfg.SFTPFolder,
		TempDir:        cfg.TempDir,
		WaitingTime:    cfg.WaitingTime,
		RetentionHours: cfg.RetentionHours,
		Src:            src,
		Store:          store,
		Sender:         sender,
		ChannelID:      cfg.ChannelID,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Dead-man switch do Kuma: heartbeat so com o gateway conectado.
	discord.StartKumaHeartbeat(ctx, session)

	go func() {
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				handler.PurgeExpiredStates(15 * time.Minute)
			}
		}
	}()

	slog.Info("duck-pbss iniciado", "servidor", cfg.ServerName, "modo", cfg.SelectFTPMode, "canal", cfg.ChannelID)
	if err := pipeline.Run(ctx); err != nil && ctx.Err() == nil {
		slog.Error("pipeline encerrado com erro", "erro", err)
		os.Exit(1)
	}
	slog.Info("encerrando duck-pbss...")
}
