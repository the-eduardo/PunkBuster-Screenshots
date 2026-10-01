package discord

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// fakeGateway sobe um servidor que responde GET /gateway com a própria URL ws://
// e, no upgrade de WebSocket, manda direto um frame de close com o código dado
// (o que o Discord faz com token recusado: close 4004). Devolve o contador de
// conexões WebSocket aceitas. Aponta discordgo.EndpointGateway pro servidor e
// restaura no Cleanup — Open() usa o endpoint de verdade, sem dublê da função.
func fakeGateway(t *testing.T, closeCode uint16, closeText string) *int32 {
	t.Helper()
	var conns int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			fmt.Fprintf(w, `{"url":"ws://%s"}`, strings.TrimPrefix(srv.URL, "http://"))
			return
		}
		atomic.AddInt32(&conns, 1)
		h, ok := w.(http.Hijacker)
		if !ok {
			t.Error("ResponseWriter sem Hijacker")
			return
		}
		c, rw, err := h.Hijack()
		if err != nil {
			t.Errorf("Hijack: %v", err)
			return
		}
		defer c.Close()
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
			base64.StdEncoding.EncodeToString(sum[:]))
		payload := append(binary.BigEndian.AppendUint16(nil, closeCode), closeText...)
		rw.Write(append([]byte{0x88, byte(len(payload))}, payload...)) // FIN+close, sem máscara (servidor)
		rw.Flush()
	}))
	t.Cleanup(srv.Close)

	oldEndpoint, oldDelays := discordgo.EndpointGateway, openRetryDelays
	discordgo.EndpointGateway = srv.URL + "/gateway"
	openRetryDelays = []time.Duration{time.Millisecond}
	t.Cleanup(func() { discordgo.EndpointGateway, openRetryDelays = oldEndpoint, oldDelays })
	return &conns
}

// TestOpenTokenRecusadoAbortaSemRetryEPreservaErrorsIs é o teste de FIAÇÃO de
// Open(): TestIsInvalidToken e TestOpenTokenInvalidoPreservaCausaViaErrorsIs
// provam as funções puras, mas nada provava que Open() as CHAMA — apagar o
// `if isInvalidToken(...)` ou trocar wrapInvalidToken(lastErr) por
// ErrInvalidToken deixava a suíte verde. Aqui o close 4004 vem de um gateway
// falso pelo caminho real (discordgo -> gorilla).
func TestOpenTokenRecusadoAbortaSemRetryEPreservaErrorsIs(t *testing.T) {
	conns := fakeGateway(t, 4004, "Authentication failed.")

	_, err := Open("token-fake")
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("errors.Is(err, ErrInvalidToken) deveria ser true; err=%v", err)
	}
	if !strings.Contains(err.Error(), "websocket: close 4004") {
		t.Fatalf("a causa original deveria continuar no texto do erro, veio: %q", err)
	}
	if got := atomic.LoadInt32(conns); got != 1 {
		t.Fatalf("token recusado deve abortar na 1a tentativa, mas houve %d conexões", got)
	}
}

// Contraprova positiva do par acima: um close que NÃO é 4004 segue o caminho de
// retry (2 tentativas com 1 delay) e NÃO vira ErrInvalidToken.
func TestOpenCloseQueNaoE4004FazRetryENaoViraTokenInvalido(t *testing.T) {
	conns := fakeGateway(t, 1011, "internal error")

	_, err := Open("token-fake")
	if err == nil {
		t.Fatal("esperava erro")
	}
	if errors.Is(err, ErrInvalidToken) {
		t.Fatalf("close 1011 não pode virar ErrInvalidToken; err=%v", err)
	}
	if got := atomic.LoadInt32(conns); got != 2 {
		t.Fatalf("esperava 2 tentativas (1 + 1 retry), houve %d", got)
	}
}
