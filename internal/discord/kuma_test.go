package discord

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestStartKumaHeartbeatStatusNaoOKNaoContaComoSucesso prova que um push
// aceito pelo transporte (err == nil) mas recusado pelo servidor (404, como o
// Kuma devolve pra token de push vencido/desconhecido) NAO e tratado como
// pulso saudavel: tem que gerar WARN sem vazar a URL (que carrega o token), e
// esse WARN tem que respeitar o dedup existente (uma vez por sequencia de
// falhas) e voltar a disparar quando o servidor volta a recusar depois de um
// sucesso no meio.
func TestStartKumaHeartbeatStatusNaoOKNaoContaComoSucesso(t *testing.T) {
	var hits atomic.Int64
	var statusAtual atomic.Int32
	statusAtual.Store(http.StatusNotFound)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(int(statusAtual.Load()))
	}))
	defer srv.Close()

	t.Setenv("KUMA_PUSH_URL", srv.URL)

	orig := kumaHeartbeatInterval
	kumaHeartbeatInterval = 20 * time.Millisecond
	defer func() { kumaHeartbeatInterval = orig }()

	var buf bytes.Buffer
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(origLogger)

	session := &discordgo.Session{}
	session.DataReady = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartKumaHeartbeat(ctx, session, nil)

	// Dedup: varios tiques com 404 devem gerar so 1 WARN.
	deadline := time.Now().Add(500 * time.Millisecond)
	for hits.Load() < 5 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	logado := buf.String()
	if !strings.Contains(logado, "push do Kuma recusado") {
		t.Fatalf("esperava WARN de push recusado pelo servidor, log vazio/sem o texto: %q", logado)
	}
	if strings.Contains(logado, srv.URL) {
		t.Fatalf("o log NUNCA deve conter a URL do push (carrega o token do Kuma): %q", logado)
	}
	if n := strings.Count(logado, "push do Kuma recusado"); n != 1 {
		t.Fatalf("esperava exatamente 1 WARN (dedup por sequencia de falhas), veio %d: %q", n, logado)
	}

	// Servidor volta a aceitar: warned deve resetar sem novo log.
	statusAtual.Store(http.StatusOK)
	buf.Reset()
	time.Sleep(200 * time.Millisecond)
	if strings.Contains(buf.String(), "push do Kuma recusado") {
		t.Fatalf("servidor respondendo 200 nao deveria gerar WARN: %q", buf.String())
	}

	// E volta a recusar: como "warned" resetou no sucesso, tem que avisar de novo.
	statusAtual.Store(http.StatusNotFound)
	buf.Reset()
	deadline = time.Now().Add(500 * time.Millisecond)
	for !strings.Contains(buf.String(), "push do Kuma recusado") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(buf.String(), "push do Kuma recusado") {
		t.Fatalf("apos um sucesso no meio, uma nova recusa deveria voltar a avisar: %q", buf.String())
	}
}
