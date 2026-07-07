// Package discord mantém a sessão persistente com o Discord, a fila de envio de
// screenshots e os slash commands do mini-dashboard.
package discord

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// ErrInvalidToken é retornado por Open quando o Discord recusa o token (close code 4004).
// Diferente de um erro transitório de rede, tentar de novo não resolve — é preciso
// gerar um token novo no Developer Portal.
var ErrInvalidToken = fmt.Errorf("token do bot inválido ou revogado (Discord close 4004)")

// Open cria e abre uma sessão persistente com o Discord. A sessão deve ser aberta
// uma única vez no início do processo, não a cada ciclo de polling.
func Open(token string) (*discordgo.Session, error) {
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, fmt.Errorf("falha ao criar sessão do discord: %w", err)
	}
	session.Identify.Intents = discordgo.IntentsGuilds

	if err := session.Open(); err != nil {
		if strings.Contains(err.Error(), "4004") {
			return nil, ErrInvalidToken
		}
		return nil, fmt.Errorf("falha ao abrir conexão com o discord: %w", err)
	}

	session.AddHandler(func(_ *discordgo.Session, event *discordgo.Disconnect) {
		slog.Warn("sessão do discord desconectada, discordgo tentará reconectar automaticamente")
	})

	return session, nil
}
