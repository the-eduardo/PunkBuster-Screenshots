package commands

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"pbss/internal/storage"
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

func capturaLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return &buf
}

// TestHandleComponentLogaFalhaDaInteracao exercita a fiação real de
// handleComponent no ramo "busca expirada" (mensagem sem state registrado):
// antes desta mudança a chamada a s.InteractionRespond descartava o erro
// diretamente, sem passar pelo helper respond(). A mutação que derruba este
// teste é trocar a chamada a respond(...) de volta para s.InteractionRespond(...) cru.
func TestHandleComponentLogaFalhaDaInteracao(t *testing.T) {
	logBuf := capturaLog(t)

	session, err := discordgo.New("Bot faketoken")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	session.Client.Transport = erroDeRedeTransport{}

	h := &Handler{states: make(map[string]*searchState)}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID:      "1",
		Token:   "tok-fake",
		Type:    discordgo.InteractionMessageComponent,
		Message: &discordgo.Message{ID: "m-inexistente"},
		Data:    discordgo.MessageComponentInteractionData{CustomID: "pbss_next"},
	}}

	h.HandleInteraction(session, i)

	if !strings.Contains(logBuf.String(), "falha ao responder interacao") {
		t.Fatalf("esperava log de erro no ramo de busca expirada, log veio: %q", logBuf.String())
	}
}

// TestHandleComponentAvancaPaginaLogaFalhaDaInteracao cobre o segundo ponto
// mudo de handleComponent (o InteractionRespond que efetivamente atualiza a
// mensagem com a nova página), agora usando um state real registrado.
func TestHandleComponentAvancaPaginaLogaFalhaDaInteracao(t *testing.T) {
	logBuf := capturaLog(t)

	session, err := discordgo.New("Bot faketoken")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	session.Client.Transport = erroDeRedeTransport{}

	results := make([]storage.ScreenshotRecord, pageSize+1)
	for idx := range results {
		results[idx] = storage.ScreenshotRecord{GUID: "*5416a6f4ea15c7a4782f4bf64dab0182*", PlayerName: "jogador"}
	}
	h := &Handler{states: map[string]*searchState{
		"m1": {query: "jogador", results: results, page: 0, createdAt: time.Now()},
	}}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID:      "1",
		Token:   "tok-fake",
		Type:    discordgo.InteractionMessageComponent,
		Message: &discordgo.Message{ID: "m1"},
		Data:    discordgo.MessageComponentInteractionData{CustomID: "pbss_next"},
	}}

	h.HandleInteraction(session, i)

	if !strings.Contains(logBuf.String(), "falha ao responder interacao") {
		t.Fatalf("esperava log de erro ao avancar pagina, log veio: %q", logBuf.String())
	}
}

// TestRunSearchLogaFalhaDaInteracao cobre o terceiro ponto mudo: a resposta
// inicial de /pbss search. Usa storage real (SearchByGUID de verdade) em vez
// de mock, pra exercitar runSearch por inteiro via HandleInteraction.
func TestRunSearchLogaFalhaDaInteracao(t *testing.T) {
	logBuf := capturaLog(t)

	st, err := storage.Open(filepath.Join(t.TempDir(), "pbss.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer st.Close()

	const guid = "*5416a6f4ea15c7a4782f4bf64dab0182*"
	if err := st.RecordScreenshot(storage.ScreenshotRecord{GUID: guid, PlayerName: "jogador"}); err != nil {
		t.Fatalf("RecordScreenshot: %v", err)
	}

	session, err := discordgo.New("Bot faketoken")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	session.Client.Transport = erroDeRedeTransport{}

	h := NewHandler(st)
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID:    "1",
		Token: "tok-fake",
		Type:  discordgo.InteractionApplicationCommand,
		Data: discordgo.ApplicationCommandInteractionData{
			Name: "pbss",
			Options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "search", Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{Name: "termo", Type: discordgo.ApplicationCommandOptionString, Value: guid},
				}},
			},
		},
	}}

	h.HandleInteraction(session, i)

	if !strings.Contains(logBuf.String(), "falha ao responder interacao") {
		t.Fatalf("esperava log de erro ao responder /pbss search, log veio: %q", logBuf.String())
	}
}
