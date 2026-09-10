package commands

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestPurgeExpiredStatesRemovesOnlyOldEntries(t *testing.T) {
	h := &Handler{states: make(map[string]*searchState)}

	h.states["old"] = &searchState{query: "old", createdAt: time.Now().Add(-20 * time.Minute)}
	h.states["fresh"] = &searchState{query: "fresh", createdAt: time.Now()}

	h.PurgeExpiredStates(15 * time.Minute)

	if _, ok := h.states["old"]; ok {
		t.Error("estado com mais de maxAge deveria ter sido removido")
	}
	if _, ok := h.states["fresh"]; !ok {
		t.Error("estado recente nao deveria ter sido removido")
	}
}

func TestPurgeExpiredStatesKeepsManyFreshEntries(t *testing.T) {
	h := &Handler{states: make(map[string]*searchState)}

	for i := 0; i < 600; i++ {
		h.states[string(rune(i))] = &searchState{createdAt: time.Now()}
	}

	h.PurgeExpiredStates(15 * time.Minute)

	if len(h.states) != 600 {
		t.Errorf("esperava manter as 600 entradas recentes (mesmo acima do antigo cap de 500), tinha %d", len(h.states))
	}
}

// erroDeRedeTransport simula uma falha de transporte (timeout, DNS, etc.) sem
// bater na rede de verdade: RoundTrip nunca chega a montar uma conexão.
type erroDeRedeTransport struct{}

func (erroDeRedeTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("rede indisponivel (simulado)")
}

// TestRespondEphemeralLogaFalhaDaInteracao exercita a fiação real de
// respondEphemeral (chamado por runLast, runSearch etc.): quando
// s.InteractionRespond falha — o Discord pode rejeitar por qualquer limite
// além do que embedDescBudget cobre, ou por falha de rede — o erro não pode
// ser descartado em silêncio, senão o usuário só vê "The application did not
// respond" sem nada no log pra investigar depois. A mutação que derruba este
// teste é remover o "if err != nil { slog.Error(...) }" em respondEphemeral.
func TestRespondEphemeralLogaFalhaDaInteracao(t *testing.T) {
	var logBuf bytes.Buffer
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(origLogger)

	session, err := discordgo.New("Bot faketoken")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	session.Client.Transport = erroDeRedeTransport{}

	h := &Handler{}
	interaction := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID:    "123",
		Token: "tok-fake",
	}}

	h.respondEphemeral(session, interaction, "oi", nil, nil)

	if !strings.Contains(logBuf.String(), "falha ao responder interacao") {
		t.Fatalf("esperava log de erro ao falhar InteractionRespond, log veio: %q", logBuf.String())
	}
}

// TestHandleInteractionRecuperaDePanicNoHandler prova que um panic dentro da
// árvore de handlers (aqui, indexação sub.Options[0] em handleCommand com um
// payload malformado — sem a opção obrigatória "termo") não mata o processo:
// HandleInteraction recupera, loga com stack e responde de forma best-effort.
// Mutação: remover o defer/recover de HandleInteraction faz o panic subir e
// abortar o binário de teste (pacote inteiro fica FAIL). Segunda mutação:
// trocar o corpo do recover por "_ = recover()" faz este teste falhar na
// asserção da mensagem de log.
func TestHandleInteractionRecuperaDePanicNoHandler(t *testing.T) {
	var logBuf bytes.Buffer
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(origLogger)

	session, err := discordgo.New("Bot faketoken")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	session.Client.Transport = erroDeRedeTransport{}

	h := &Handler{states: make(map[string]*searchState)}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID:    "1",
		Token: "tok-fake",
		Type:  discordgo.InteractionApplicationCommand,
		Data: discordgo.ApplicationCommandInteractionData{
			Name: "pbss",
			Options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "search", Options: nil}, // sem a opcao obrigatoria "termo"
			},
		},
	}}

	h.HandleInteraction(session, i) // sem o fix, sub.Options[0] em handleCommand panica aqui

	if !strings.Contains(logBuf.String(), "panic no handler de interacao") {
		t.Fatalf("esperava log de panic recuperado, log veio: %q", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "index out of range") {
		t.Fatalf("esperava o valor original do panic preservado no log, log veio: %q", logBuf.String())
	}
}
