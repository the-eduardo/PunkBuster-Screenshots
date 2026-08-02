// Package discord mantém a sessão persistente com o Discord, a fila de envio de
// screenshots e os slash commands do mini-dashboard.
package discord

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// openRetryDelays define o backoff da abertura da sessao. Teto de ~46s: mais que
// isso e melhor deixar o restart:always do compose assumir.
var openRetryDelays = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 16 * time.Second}

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

	// Retry com backoff: falha de rede na abertura e transitoria (1 crash real
	// por TLS timeout em 28/07/2026) e nao merece derrubar o processo. Token
	// recusado (4004) aborta na hora — insistir nao muda o resultado.
	var lastErr error
	for attempt := 0; ; attempt++ {
		lastErr = session.Open()
		if lastErr == nil {
			break
		}
		if strings.Contains(lastErr.Error(), "4004") {
			return nil, ErrInvalidToken
		}
		if attempt >= len(openRetryDelays) {
			return nil, fmt.Errorf("falha ao abrir conexão com o discord após %d tentativas: %w", attempt+1, lastErr)
		}
		slog.Warn("falha ao abrir conexão com o discord, tentando de novo",
			"tentativa", attempt+1,
			"de", len(openRetryDelays)+1,
			"espera", openRetryDelays[attempt],
			"erro", lastErr)
		time.Sleep(openRetryDelays[attempt])
	}

	session.AddHandler(func(_ *discordgo.Session, event *discordgo.Disconnect) {
		slog.Warn("sessão do discord desconectada, discordgo tentará reconectar automaticamente")
	})

	return session, nil
}
