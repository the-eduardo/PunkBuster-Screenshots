package discord

import (
	"errors"
	"testing"
)

// TestIsInvalidToken prova que a detecção do close 4004 casa pelo prefixo que
// o gorilla/websocket imprime ("websocket: close 4004: ..."), não por "4004"
// solto no texto — um net.OpError de read/write inclui a porta local efêmera
// (ex. "...172.17.0.2:40044..."), e vários valores dessa faixa contêm "4004"
// por coincidência.
func TestIsInvalidToken(t *testing.T) {
	casos := []struct {
		nome string
		erro string
		want bool
	}{
		{
			nome: "close 4004 real do gateway",
			erro: "websocket: close 4004: Authentication failed.",
			want: true,
		},
		{
			nome: "net.OpError com porta efemera contendo 4004 por coincidencia",
			erro: "read tcp 172.17.0.2:40044->162.159.128.233:443: read: connection reset by peer",
			want: false,
		},
		{
			nome: "close code diferente (1006, abnormal closure)",
			erro: "websocket: close 1006 (abnormal closure): unexpected EOF",
			want: false,
		},
		{
			nome: "timeout na resolucao do gateway, sem nenhum close code",
			erro: `Get "https://discord.com/api/v9/gateway/bot": dial tcp: i/o timeout`,
			want: false,
		},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got := isInvalidToken(errors.New(c.erro))
			if got != c.want {
				t.Fatalf("isInvalidToken(%q) = %v, want %v", c.erro, got, c.want)
			}
		})
	}
}

// TestOpenTokenInvalidoPreservaCausaViaErrorsIs prova a fiação: Open() chama
// wrapInvalidToken (não uma cópia paralela da expressão) quando isInvalidToken
// diz que é close 4004, e wrapInvalidToken precisa envolver ErrInvalidToken
// com %w (não %v), senão o errors.Is(err, discord.ErrInvalidToken) do call
// site em cmd/bot/main.go nunca casa e o operador perde a mensagem que
// distingue "token revogado" de "falha de rede genérica". Se alguém trocar o
// %w por %v em wrapInvalidToken, este teste falha.
func TestOpenTokenInvalidoPreservaCausaViaErrorsIs(t *testing.T) {
	causa := errors.New("websocket: close 4004: Authentication failed.")
	err := wrapInvalidToken(causa)

	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("errors.Is(err, ErrInvalidToken) deveria ser true; err=%v", err)
	}
	if got := err.Error(); got == ErrInvalidToken.Error() {
		t.Fatalf("a causa original (%q) deveria continuar visível no texto do erro, veio só: %q", causa, got)
	}
}
