package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"

	"pbss/internal/storage"
)

// Testes de FIAÇÃO da leitura das options do /pbss por NOME (16/11/2026).
//
// O Discord entrega as options do subcomando na ordem em que o USUÁRIO as
// preencheu, não na ordem em que pbssCommand() as declara (discordgo #1126).
// handleCommand lia por posição (sub.Options[0] = termo, sub.Options[1] =
// quantidade): quem escolhesse "quantidade" antes de "termo" recebia
// Options[0] com Type Integer, StringValue() entrava em panic, o recover de
// HandleInteraction devolvia "Erro interno ao processar o comando" e um
// comando perfeitamente válido falhava.
//
// Os testes passam pelo caminho real (HandleInteraction → handleCommand →
// runLast/runSearch) e julgam o payload que IRIA pro Discord, não uma função
// pura: é a fiação que estava quebrada, não o formatador.

// interacaoPbss monta a interação do /pbss com as options na ordem dada.
func interacaoPbss(sub string, opts ...*discordgo.ApplicationCommandInteractionDataOption) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID: "1", Token: "tok-fake", Type: discordgo.InteractionApplicationCommand,
		Data: discordgo.ApplicationCommandInteractionData{
			Name: "pbss",
			Options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: sub, Options: opts},
			},
		},
	}}
}

// capturaPrimeiroCorpoTransport responde 204 a toda requisição e guarda o
// corpo da PRIMEIRA que tiver corpo — ao contrário do capturaCorpoTransport,
// que guarda a última e é sobrescrito pelo GET seguinte do runSearch.
type capturaPrimeiroCorpoTransport struct {
	mu    sync.Mutex
	corpo []byte
}

func (c *capturaPrimeiroCorpoTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var b []byte
	if r.Body != nil {
		b, _ = io.ReadAll(r.Body)
	}
	c.mu.Lock()
	if len(c.corpo) == 0 && len(b) > 0 {
		c.corpo = b
	}
	c.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Status:     "204 No Content",
		Body:       io.NopCloser(bytes.NewReader(nil)),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

// titulosDosEmbeds decodifica só o que o teste julga. Não dá pra fazer
// Unmarshal em discordgo.InteractionResponse aqui: o payload do /pbss search
// traz components, e MessageComponent é uma interface sem decodificador
// próprio — o unmarshal falharia antes de chegar nos embeds.
func (c *capturaPrimeiroCorpoTransport) titulosDosEmbeds(t *testing.T) []string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.corpo) == 0 {
		t.Fatal("nenhuma requisição com corpo chegou ao transporte: a interação não foi respondida")
	}
	var payload struct {
		Data struct {
			Content string `json:"content"`
			Embeds  []struct {
				Title string `json:"title"`
			} `json:"embeds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(c.corpo, &payload); err != nil {
		t.Fatalf("corpo da resposta não é JSON de interação: %v (%q)", err, c.corpo)
	}
	if payload.Data.Content != "" {
		t.Fatalf("esperava resposta com embed, veio Content=%q", payload.Data.Content)
	}
	out := make([]string, 0, len(payload.Data.Embeds))
	for _, e := range payload.Data.Embeds {
		out = append(out, e.Title)
	}
	return out
}

func sessaoCapturandoPrimeira(t *testing.T) (*discordgo.Session, *capturaPrimeiroCorpoTransport) {
	t.Helper()
	s, err := discordgo.New("Bot faketoken")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	tr := &capturaPrimeiroCorpoTransport{}
	s.Client.Transport = tr
	return s, tr
}

// storeComScreenshots devolve um Store com n linhas do mesmo jogador. GUID e
// filename distintos porque o nome do arquivo RECICLA em produção (teto de
// 10000 no PunkBuster) e não identifica a linha.
func storeComScreenshots(t *testing.T, jogador string, n int) *storage.Store {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "pbss.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	for k := 0; k < n; k++ {
		if err := st.RecordScreenshot(storage.ScreenshotRecord{
			GUID:       fmt.Sprintf("*%032x*", k),
			PlayerName: jogador,
			FileName:   fmt.Sprintf("pb%06d.png", k),
			Server:     "srv",
		}); err != nil {
			t.Fatalf("RecordScreenshot %d: %v", k, err)
		}
	}
	return st
}

// TestRunLastOptionsForaDeOrdem é o caso que o acesso posicional quebra: o
// usuário preencheu "quantidade" antes de "termo". Com a leitura por posição,
// StringValue() sobre a option Integer panica, o recover responde o texto
// genérico de erro e nenhum embed sai — o teste falha no len(Embeds).
func TestRunLastOptionsForaDeOrdem(t *testing.T) {
	const jogador = "JoseToalha"
	st := storeComScreenshots(t, jogador, 25)

	session, tr := sessaoCapturando(t)
	h := NewHandler(st)
	// Ordem INVERTIDA de propósito: quantidade primeiro, termo depois. O
	// discordgo decodifica option inteira do JSON como float64.
	h.HandleInteraction(session, interacaoPbss("last",
		&discordgo.ApplicationCommandInteractionDataOption{Name: "quantidade", Type: discordgo.ApplicationCommandOptionInteger, Value: float64(7)},
		&discordgo.ApplicationCommandInteractionDataOption{Name: "termo", Type: discordgo.ApplicationCommandOptionString, Value: jogador},
	))

	resp := tr.resposta(t)
	if resp.Data == nil || len(resp.Data.Embeds) != 1 {
		t.Fatalf("com as options fora de ordem o /pbss last deveria responder 1 embed; veio %+v", resp.Data)
	}
	if got := resp.Data.Embeds[0].Title; !strings.HasPrefix(got, "Últimos 7 screenshots") {
		t.Fatalf("quantidade:7 lida fora de ordem deveria render 7 resultados; título do payload real: %q", got)
	}
	if n := strings.Count(resp.Data.Embeds[0].Description, "\n"); n != 7 {
		t.Fatalf("título promete 7 mas a descrição tem %d linhas", n)
	}
}

// TestRunLastOptionsNaOrdemDeclarada é a contraprova: ler por nome não pode
// mudar nada no caso normal (termo primeiro, quantidade depois).
func TestRunLastOptionsNaOrdemDeclarada(t *testing.T) {
	const jogador = "JoseToalha"
	st := storeComScreenshots(t, jogador, 25)

	session, tr := sessaoCapturando(t)
	h := NewHandler(st)
	h.HandleInteraction(session, interacaoPbss("last",
		&discordgo.ApplicationCommandInteractionDataOption{Name: "termo", Type: discordgo.ApplicationCommandOptionString, Value: jogador},
		&discordgo.ApplicationCommandInteractionDataOption{Name: "quantidade", Type: discordgo.ApplicationCommandOptionInteger, Value: float64(7)},
	))

	resp := tr.resposta(t)
	if resp.Data == nil || len(resp.Data.Embeds) != 1 {
		t.Fatalf("esperava 1 embed no /pbss last na ordem declarada, veio %+v", resp.Data)
	}
	if got := resp.Data.Embeds[0].Title; !strings.HasPrefix(got, "Últimos 7 screenshots") {
		t.Fatalf("título do payload real: %q", got)
	}
}

// TestRunSearchLeTermoPorNome cobre o /pbss search pelo mesmo caminho. Hoje o
// subcomando tem uma option só, então a ordem não o quebra; o teste trava o
// comportamento pra quando uma segunda option for acrescentada, que é quando o
// acesso posicional volta a morder.
func TestRunSearchLeTermoPorNome(t *testing.T) {
	const jogador = "JoseToalha"
	st := storeComScreenshots(t, jogador, 3)

	// Transporte próprio: runSearch faz DUAS chamadas (o InteractionRespond e
	// o InteractionResponse que busca o ID da mensagem pra guardar o estado de
	// paginação), e o helper compartilhado guarda só a última — que é um GET
	// sem corpo. Aqui o primeiro corpo não vazio é o que interessa.
	session, tr := sessaoCapturandoPrimeira(t)
	h := NewHandler(st)
	h.HandleInteraction(session, interacaoPbss("search",
		&discordgo.ApplicationCommandInteractionDataOption{Name: "termo", Type: discordgo.ApplicationCommandOptionString, Value: jogador},
	))

	titulos := tr.titulosDosEmbeds(t)
	if len(titulos) != 1 {
		t.Fatalf("esperava 1 embed na resposta do /pbss search, veio %v", titulos)
	}
	if titulos[0] != "Resultados para: "+jogador {
		t.Fatalf("título do payload real: %q", titulos[0])
	}
}

// Sem a option "termo", optionByName devolve nil e nada panica: o termo sai
// em branco e o lookup já tem guarda pra isso (string vazia nunca casa).
func TestRunLastTermoAusenteNaoPanica(t *testing.T) {
	st := storeComScreenshots(t, "JoseToalha", 3)

	session, tr := sessaoCapturando(t)
	h := NewHandler(st)
	h.HandleInteraction(session, interacaoPbss("last",
		&discordgo.ApplicationCommandInteractionDataOption{Name: "quantidade", Type: discordgo.ApplicationCommandOptionInteger, Value: float64(3)},
	))

	resp := tr.resposta(t)
	if resp.Data == nil {
		t.Fatal("nenhuma resposta enviada")
	}
	if len(resp.Data.Embeds) != 0 {
		t.Fatalf("sem termo nao deveria sair embed, veio %+v", resp.Data.Embeds)
	}
	if !strings.Contains(resp.Data.Content, "Nenhum screenshot encontrado") {
		t.Fatalf("esperava a resposta de busca vazia, veio: %q", resp.Data.Content)
	}
}

// TestOptionByName é o teste da função pura, complementar aos de fiação acima.
func TestOptionByName(t *testing.T) {
	opts := []*discordgo.ApplicationCommandInteractionDataOption{
		{Name: "quantidade", Type: discordgo.ApplicationCommandOptionInteger, Value: float64(9)},
		{Name: "termo", Type: discordgo.ApplicationCommandOptionString, Value: "abc"},
	}
	if o := optionByName(opts, "termo"); o == nil || o.StringValue() != "abc" {
		t.Fatalf("termo deveria ser encontrado independentemente da posição, veio %+v", o)
	}
	if o := optionByName(opts, "quantidade"); o == nil || o.IntValue() != 9 {
		t.Fatalf("quantidade deveria ser encontrada, veio %+v", o)
	}
	if o := optionByName(opts, "inexistente"); o != nil {
		t.Fatalf("option ausente deveria devolver nil, veio %+v", o)
	}
	if o := optionByName(nil, "termo"); o != nil {
		t.Fatalf("slice vazio deveria devolver nil, veio %+v", o)
	}
}
