package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
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

// opcoesTermo devolve a option "termo" de cada subcomando do /pbss que a tem,
// indexada pelo nome do subcomando.
func opcoesTermo(cmd *discordgo.ApplicationCommand) map[string]*discordgo.ApplicationCommandOption {
	termos := make(map[string]*discordgo.ApplicationCommandOption)
	for _, sub := range cmd.Options {
		for _, o := range sub.Options {
			if o.Name == "termo" {
				termos[sub.Name] = o
			}
		}
	}
	return termos
}

// ITEM 16 (enxame 29/09/2026): sem MaxLength o Discord aceita até 6000 chars
// na option "termo"; as DUAS (search e last) têm que sair com 100.
func TestPbssCommandTermoTemMaxLength100(t *testing.T) {
	termos := opcoesTermo(pbssCommand())
	for _, sub := range []string{"search", "last"} {
		o, ok := termos[sub]
		if !ok {
			t.Errorf("/pbss %s sem option termo", sub)
			continue
		}
		if o.MaxLength != 100 {
			t.Errorf("/pbss %s termo: MaxLength = %d, quero 100", sub, o.MaxLength)
		}
	}
	if len(termos) != 2 {
		t.Errorf("esperava option termo em exatamente 2 subcomandos (search, last), achei %d", len(termos))
	}
}

// capturaRequisicaoTransport guarda método, caminho e corpo da última
// requisição e responde 204, sem rede.
type capturaRequisicaoTransport struct {
	metodo, caminho string
	corpo           []byte
}

func (c *capturaRequisicaoTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.metodo, c.caminho = r.Method, r.URL.Path
	if r.Body != nil {
		c.corpo, _ = io.ReadAll(r.Body)
	}
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Status:     "204 No Content",
		Body:       io.NopCloser(bytes.NewReader(nil)),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

// Fiação: o MaxLength só vale se é o pbssCommand() que o Register manda pro
// Discord — e por POST de UM comando (upsert), nunca PUT em lote: o app é
// compartilhado com o bf4db-bot e um bulk overwrite apagaria os comandos dele.
func TestRegisterEnviaPbssCommandPorUpsertComMaxLength(t *testing.T) {
	s, err := discordgo.New("Bot faketoken")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	tr := &capturaRequisicaoTransport{}
	s.Client.Transport = tr
	s.State.User = &discordgo.User{ID: "111"}

	// O 204 sem corpo faz o discordgo reclamar no unmarshal da resposta; o que
	// importa aqui é o que SAIU, não o retorno.
	_ = NewHandler(nil).Register(s, "222")

	if tr.metodo != http.MethodPost {
		t.Fatalf("Register usou %s %s; tem que ser POST (upsert de um comando), nunca PUT em lote", tr.metodo, tr.caminho)
	}
	if !strings.HasSuffix(tr.caminho, "/applications/111/guilds/222/commands") {
		t.Fatalf("Register foi pra %q, esperava o endpoint de comandos da guild", tr.caminho)
	}
	var enviado discordgo.ApplicationCommand
	if err := json.Unmarshal(tr.corpo, &enviado); err != nil {
		t.Fatalf("corpo enviado não é ApplicationCommand: %v (%q)", err, tr.corpo)
	}
	if enviado.Name != "pbss" {
		t.Fatalf("Register enviou o comando %q, esperava pbss", enviado.Name)
	}
	termos := opcoesTermo(&enviado)
	for _, sub := range []string{"search", "last"} {
		if o, ok := termos[sub]; !ok || o.MaxLength != 100 {
			t.Errorf("payload do Register: /pbss %s termo sem MaxLength 100 (%+v)", sub, o)
		}
	}
}
