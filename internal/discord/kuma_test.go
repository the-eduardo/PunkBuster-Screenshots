package discord

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
// syncBuffer protege o buffer de log lido pelo teste enquanto a goroutine do
// heartbeat escreve nele via slog.Default() — sem o mutex, go test -race acusa
// corrida real entre o Write da goroutine e o String()/Reset() do teste
// (achado do Dev Senior no comite da drenagem de 29/08/2026).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.buf.String() }
func (s *syncBuffer) Reset()         { s.mu.Lock(); defer s.mu.Unlock(); s.buf.Reset() }

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

	buf := &syncBuffer{}
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
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

// TestStartKumaHeartbeatErroDeTransporteNaoVazaURL prova que o caminho de
// FALHA DE TRANSPORTE (err != nil do client.Get) tambem nunca loga a URL do
// push: err de transporte e' um *url.Error e seu texto embute a URL completa
// — com o token no path (achado do AppSec no comite da drenagem de
// 29/08/2026; mesma classe do incidente de 25/08/2026 que criou o pacote
// redact do BF4DB).
func TestStartKumaHeartbeatErroDeTransporteNaoVazaURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	pushURL := srv.URL
	srv.Close() // porta fechada: todo Get falha com connection refused

	t.Setenv("KUMA_PUSH_URL", pushURL)

	orig := kumaHeartbeatInterval
	kumaHeartbeatInterval = 20 * time.Millisecond
	defer func() { kumaHeartbeatInterval = orig }()

	buf := &syncBuffer{}
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	defer slog.SetDefault(origLogger)

	session := &discordgo.Session{}
	session.DataReady = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartKumaHeartbeat(ctx, session, nil)

	deadline := time.Now().Add(500 * time.Millisecond)
	for !strings.Contains(buf.String(), "push do Kuma falhou") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	logado := buf.String()
	if !strings.Contains(logado, "push do Kuma falhou") {
		t.Fatalf("esperava WARN de push falhou (porta fechada), veio: %q", logado)
	}
	if strings.Contains(logado, pushURL) {
		t.Fatalf("o log de erro de transporte NUNCA deve conter a URL do push (token): %q", logado)
	}
}

// TestStartKumaHeartbeatURLComQuebraDeLinhaNaoVazaToken cobre a variante que
// fura a redação por substituição: KUMA_PUSH_URL com \n de copy-paste vira
// erro de parse cujo %q escapa o \n — a busca pela URL crua não casa e o
// token iria inteiro pro log (medido pelo QA no comitê de 29/08/2026). Com o
// TrimSpace + redação por construção, o token não pode aparecer.
func TestStartKumaHeartbeatURLComQuebraDeLinhaNaoVazaToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	pushURL := srv.URL + "/api/push/TOKENSUPERSECRETO"
	srv.Close() // porta fechada: o Get falha mesmo com a URL limpa

	t.Setenv("KUMA_PUSH_URL", pushURL+"\n")

	orig := kumaHeartbeatInterval
	kumaHeartbeatInterval = 20 * time.Millisecond
	defer func() { kumaHeartbeatInterval = orig }()

	buf := &syncBuffer{}
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	defer slog.SetDefault(origLogger)

	session := &discordgo.Session{}
	session.DataReady = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartKumaHeartbeat(ctx, session, nil)

	deadline := time.Now().Add(500 * time.Millisecond)
	for !strings.Contains(buf.String(), "push do Kuma falhou") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	logado := buf.String()
	if !strings.Contains(logado, "push do Kuma falhou") {
		t.Fatalf("esperava WARN de push falhou, veio: %q", logado)
	}
	if strings.Contains(logado, "TOKENSUPERSECRETO") {
		t.Fatalf("token vazou no log com URL contendo quebra de linha: %q", logado)
	}
}
