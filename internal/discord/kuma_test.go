package discord

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// TestStartKumaHeartbeatSuspendeSemPollAlive prova a fiação, não só a lógica
// isolada: chama StartKumaHeartbeat de verdade (com um ticker encurtado) e
// confere que o próprio goroutine deixa de empurrar pro Kuma quando pollAlive
// diz que o poller está parado — e volta a empurrar quando ele destrava nos
// mesmos moldes do dead-man switch original. Se alguém remover a chamada a
// pollAlive() do laço, este teste falha.
func TestStartKumaHeartbeatSuspendeSemPollAlive(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("KUMA_PUSH_URL", srv.URL)

	orig := kumaHeartbeatInterval
	kumaHeartbeatInterval = 20 * time.Millisecond
	defer func() { kumaHeartbeatInterval = orig }()

	var pollerVivo atomic.Bool // começa false: poller "travado"
	session := &discordgo.Session{}
	session.DataReady = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartKumaHeartbeat(ctx, session, func() bool { return pollerVivo.Load() })

	// Poller travado: nenhum push deve sair, mesmo com o gateway conectado.
	time.Sleep(200 * time.Millisecond)
	if got := hits.Load(); got != 0 {
		t.Fatalf("pollAlive=false deveria suspender o pulso do Kuma, mas recebeu %d hit(s)", got)
	}

	// Poller destrava: o pulso deve voltar sem precisar reiniciar o processo.
	pollerVivo.Store(true)
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := hits.Load(); got == 0 {
		t.Fatalf("pollAlive=true deveria liberar o pulso do Kuma, mas nao recebeu nenhum hit")
	}
}

// TestStartKumaHeartbeatPollAliveNilMantemComportamentoAntigo garante que
// pollAlive == nil (chamador que não montou pipeline nenhum, ex. outro
// binário de teste/ferramenta) preserva o comportamento anterior à mudança:
// push liberado só pelo DataReady.
func TestStartKumaHeartbeatPollAliveNilMantemComportamentoAntigo(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("KUMA_PUSH_URL", srv.URL)

	orig := kumaHeartbeatInterval
	kumaHeartbeatInterval = 20 * time.Millisecond
	defer func() { kumaHeartbeatInterval = orig }()

	session := &discordgo.Session{}
	session.DataReady = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartKumaHeartbeat(ctx, session, nil)

	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := hits.Load(); got == 0 {
		t.Fatalf("pollAlive nil deveria manter o push liberado so pelo DataReady, mas nao recebeu nenhum hit")
	}
}
