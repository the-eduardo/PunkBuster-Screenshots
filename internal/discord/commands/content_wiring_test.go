package commands

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"pbss/internal/storage"
)

// TestRunSearchTermoLongoSaiClampado é o teste de FIAÇÃO do clampContent em
// respondEphemeral (drenagem 25/09/2026): os testes de clampContent provam a
// função pura, mas nada provava que o Content que sai no payload real passa
// por ela. /pbss search com termo de 5000 chars cai no ramo "nenhum
// resultado", que ecoa o termo cru — o Content capturado tem que caber em
// 2000 e trazer o aviso de truncamento.
func TestRunSearchTermoLongoSaiClampado(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "pbss.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer st.Close()

	session, tr := sessaoCapturando(t)
	h := NewHandler(st)
	termo := strings.Repeat("x", 5000)
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID: "1", Token: "tok-fake", Type: discordgo.InteractionApplicationCommand,
		Data: discordgo.ApplicationCommandInteractionData{
			Name: "pbss",
			Options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "search", Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{Name: "termo", Type: discordgo.ApplicationCommandOptionString, Value: termo},
				}},
			},
		},
	}}

	h.HandleInteraction(session, i)

	resp := tr.resposta(t)
	if resp.Data == nil {
		t.Fatal("resposta sem Data")
	}
	if !strings.HasPrefix(resp.Data.Content, "Nenhum screenshot encontrado") {
		t.Fatalf("esperava o ramo 'nenhum resultado', veio: %.80q", resp.Data.Content)
	}
	if n := len([]rune(resp.Data.Content)); n > 2000 {
		t.Fatalf("Content do payload real estourou o teto de 2000 do Discord: %d chars", n)
	}
	if !strings.Contains(resp.Data.Content, "(termo truncado)") {
		t.Fatalf("esperava o aviso de truncamento no Content real, veio sufixo: %q", resp.Data.Content[len(resp.Data.Content)-40:])
	}
}
