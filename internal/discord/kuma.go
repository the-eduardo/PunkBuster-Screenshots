package discord

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/bwmarrin/discordgo"
)

// StartKumaHeartbeat liga o dead-man switch do Uptime Kuma (item 5 da auditoria
// de observabilidade).
//
// O monitor docker "duck_pbss" fica UP mesmo com o bot incapaz de falar com o
// Discord (token revogado, gateway em backoff infinito). Aqui o push so sai com
// s.DataReady == true, ou seja, com o websocket efetivamente conectado.
//
// Sem KUMA_PUSH_URL no ambiente e no-op.
func StartKumaHeartbeat(ctx context.Context, s *discordgo.Session) {
	url := os.Getenv("KUMA_PUSH_URL")
	if url == "" {
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		warned := false
		for {
			if s.DataReady {
				resp, err := client.Get(url + "?status=up&msg=gateway+ok")
				if err != nil {
					// Loga uma vez por sequencia de falhas, pra nao poluir o log.
					if !warned {
						slog.Warn("push do Kuma falhou", "erro", err)
						warned = true
					}
				} else {
					warned = false
					_ = resp.Body.Close()
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}
