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

// capturaCorpoTransport responde 204 a toda requisição e guarda o corpo da
// última, pra o teste inspecionar o payload que IRIA pro Discord.
type capturaCorpoTransport struct {
	mu    sync.Mutex
	corpo []byte
}

func (c *capturaCorpoTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var b []byte
	if r.Body != nil {
		b, _ = io.ReadAll(r.Body)
	}
	c.mu.Lock()
	c.corpo = b
	c.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Status:     "204 No Content",
		Body:       io.NopCloser(bytes.NewReader(nil)),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

func (c *capturaCorpoTransport) resposta(t *testing.T) discordgo.InteractionResponse {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.corpo) == 0 {
		t.Fatal("nenhuma requisição chegou ao transporte: a interação não foi respondida")
	}
	var resp discordgo.InteractionResponse
	if err := json.Unmarshal(c.corpo, &resp); err != nil {
		t.Fatalf("corpo da resposta não é JSON de InteractionResponse: %v (%q)", err, c.corpo)
	}
	return resp
}

func sessaoCapturando(t *testing.T) (*discordgo.Session, *capturaCorpoTransport) {
	t.Helper()
	s, err := discordgo.New("Bot faketoken")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	tr := &capturaCorpoTransport{}
	s.Client.Transport = tr
	return s, tr
}

// TestRunStatsUsaFieldClampado é o teste de FIAÇÃO do clamp do field "Top 10"
// (drenagem 25/09/2026): os testes de buildStatsEmbed/formatTopPlayers provam
// a função pura, mas nada provava que o /pbss stats de verdade (runStats via
// HandleInteraction) monta o embed por ela. Top10 com nomes longos: o field
// que sai no payload tem que caber em 1024 e trazer o aviso de omissão.
func TestRunStatsUsaFieldClampado(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "pbss.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer st.Close()

	nomeLongo := strings.Repeat("N", 200)
	for g := 0; g < 10; g++ {
		guid := fmt.Sprintf("*%032x*", g)
		for n := 0; n <= g; n++ {
			if err := st.RecordScreenshot(storage.ScreenshotRecord{
				GUID: guid, PlayerName: nomeLongo, FileName: fmt.Sprintf("%d-%d.png", g, n), Server: "srv",
			}); err != nil {
				t.Fatalf("RecordScreenshot: %v", err)
			}
		}
	}

	session, tr := sessaoCapturando(t)
	h := NewHandler(st)
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID: "1", Token: "tok-fake", Type: discordgo.InteractionApplicationCommand,
		Data: discordgo.ApplicationCommandInteractionData{
			Name:    "pbss",
			Options: []*discordgo.ApplicationCommandInteractionDataOption{{Name: "stats"}},
		},
	}}

	h.HandleInteraction(session, i)

	resp := tr.resposta(t)
	if resp.Data == nil || len(resp.Data.Embeds) != 1 {
		t.Fatalf("esperava 1 embed na resposta do /pbss stats, veio %+v", resp.Data)
	}
	var valor string
	achou := false
	for _, f := range resp.Data.Embeds[0].Fields {
		if f.Name == "Top 10 mais flagrados" {
			valor, achou = f.Value, true
		}
	}
	if !achou {
		t.Fatalf("field Top 10 ausente no payload: %+v", resp.Data.Embeds[0].Fields)
	}
	if len(valor) > 1024 {
		t.Fatalf("field Top 10 do payload real estourou o limite do Discord: %d bytes", len(valor))
	}
	if !strings.Contains(valor, "omitido") {
		t.Fatalf("esperava o aviso de omissão no field do payload real, veio: %q", valor)
	}
}
