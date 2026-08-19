package discord

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/bwmarrin/discordgo"
)

// kumaHeartbeatInterval é var (não const) pra teste poder encurtar o tique sem
// esperar 5 minutos de verdade.
var kumaHeartbeatInterval = 5 * time.Minute

// StartKumaHeartbeat liga o dead-man switch do Uptime Kuma (item 5 da auditoria
// de observabilidade).
//
// O monitor docker "duck_pbss" fica UP mesmo com o bot incapaz de falar com o
// Discord (token revogado, gateway em backoff infinito). Aqui o push so sai com
// s.DataReady == true, ou seja, com o websocket efetivamente conectado — e com
// pollAlive() true, ou seja, com o poller de screenshots (SFTP/FTP) ainda
// listando o diretório remoto normalmente. Sem essa segunda checagem, um
// poller travado (EnsureConnected em loop, List preso) fica invisível: o
// gateway segue conectado e o Kuma segue recebendo "up" sem ninguém avisado.
//
// pollAlive pode ser nil (ex. em testes que não montam o pipeline inteiro),
// caso em que só a checagem do gateway vale.
//
// Sem KUMA_PUSH_URL no ambiente e no-op.
func StartKumaHeartbeat(ctx context.Context, s *discordgo.Session, pollAlive func() bool) {
	url := os.Getenv("KUMA_PUSH_URL")
	if url == "" {
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	go func() {
		t := time.NewTicker(kumaHeartbeatInterval)
		defer t.Stop()
		warned := false
		pollWarned := false
		for {
			alive := pollAlive == nil || pollAlive()
			if !alive && !pollWarned {
				slog.Warn("poller sem List() recente, suspendendo pulso do Kuma")
				pollWarned = true
			} else if alive {
				pollWarned = false
			}
			if s.DataReady && alive {
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
