package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"pbss/internal/storage"
)

// renderPage e formatEntries são funções puras que decidem quantos itens
// aparecem na página, o rodapé "Página X/Y" e — o mais frágil — se os botões
// Anterior/Próxima ficam desabilitados. É aritmética de índice com pageSize
// fixo, exatamente o lugar onde um off-by-one passa despercebido: o sintoma
// seria um botão "Próxima" clicável na última página, que não faz nada.

func registros(n int) []storage.ScreenshotRecord {
	out := make([]storage.ScreenshotRecord, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, storage.ScreenshotRecord{
			GUID:       "guid",
			PlayerName: "Jogador",
			FileName:   "pb.png",
			CapturedAt: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
		})
	}
	return out
}

// botoes devolve (anteriorDesabilitado, proximaDesabilitada) da linha de ação.
func botoes(t *testing.T, comps []discordgo.MessageComponent) (bool, bool) {
	t.Helper()
	row, ok := comps[0].(discordgo.ActionsRow)
	if !ok {
		t.Fatalf("esperava ActionsRow, veio %T", comps[0])
	}
	prev, ok := row.Components[0].(discordgo.Button)
	if !ok {
		t.Fatalf("esperava Button em Anterior, veio %T", row.Components[0])
	}
	next, ok := row.Components[1].(discordgo.Button)
	if !ok {
		t.Fatalf("esperava Button em Proxima, veio %T", row.Components[1])
	}
	return prev.Disabled, next.Disabled
}

func contaLinhas(desc string) int {
	return len(strings.Split(strings.TrimRight(desc, "\n"), "\n"))
}

// Múltiplo exato de pageSize é o caso que pega o off-by-one: com 10 resultados
// e pageSize 5, a página 2 (índice 1) é a ÚLTIMA, e "Próxima" tem que estar
// desabilitada mesmo com (page+1)*pageSize == len(results).
func TestRenderPageMultiploExatoDesabilitaProximaNaUltima(t *testing.T) {
	res := registros(10)

	_, comps := renderPage(&searchState{query: "q", results: res, page: 0})
	prevDis, nextDis := botoes(t, comps)
	if !prevDis {
		t.Error("na primeira pagina, Anterior deveria estar desabilitado")
	}
	if nextDis {
		t.Error("na primeira pagina de 2, Proxima NAO deveria estar desabilitada")
	}

	embed, comps := renderPage(&searchState{query: "q", results: res, page: 1})
	prevDis, nextDis = botoes(t, comps)
	if prevDis {
		t.Error("na segunda pagina, Anterior deveria estar habilitado")
	}
	if !nextDis {
		t.Error("na ULTIMA pagina, Proxima deveria estar desabilitada (off-by-one)")
	}
	if n := contaLinhas(embed.Description); n != 5 {
		t.Errorf("ultima pagina cheia deveria ter 5 itens, veio %d", n)
	}
	if want := "Página 2/2 — 10 resultado(s)"; embed.Footer.Text != want {
		t.Errorf("rodape errado: %q (queria %q)", embed.Footer.Text, want)
	}
}

func TestRenderPageUltimaPaginaParcial(t *testing.T) {
	res := registros(7)

	embed, comps := renderPage(&searchState{query: "q", results: res, page: 1})
	_, nextDis := botoes(t, comps)
	if !nextDis {
		t.Error("com 7 resultados a pagina 2 e a ultima, Proxima deveria estar desabilitada")
	}
	if n := contaLinhas(embed.Description); n != 2 {
		t.Errorf("pagina parcial deveria ter 2 itens, veio %d", n)
	}
	if want := "Página 2/2 — 7 resultado(s)"; embed.Footer.Text != want {
		t.Errorf("rodape errado: %q (queria %q)", embed.Footer.Text, want)
	}
}

func TestRenderPageUmaPaginaSoDesabilitaOsDoisBotoes(t *testing.T) {
	embed, comps := renderPage(&searchState{query: "q", results: registros(3), page: 0})
	prevDis, nextDis := botoes(t, comps)
	if !prevDis || !nextDis {
		t.Errorf("com uma pagina so, os dois botoes deveriam estar desabilitados (prev=%v next=%v)", prevDis, nextDis)
	}
	if want := "Página 1/1 — 3 resultado(s)"; embed.Footer.Text != want {
		t.Errorf("rodape errado: %q (queria %q)", embed.Footer.Text, want)
	}
}

func TestFormatEntriesLinkSoComOsTresCamposPreenchidos(t *testing.T) {
	completo := storage.ScreenshotRecord{
		GUID: "abc", PlayerName: "Jogador",
		CapturedAt:       time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
		DiscordGuildID:   "1",
		DiscordChannelID: "2",
		DiscordMessageID: "3",
	}
	if out := formatEntries([]storage.ScreenshotRecord{completo}); !strings.Contains(out, "https://discord.com/channels/1/2/3") {
		t.Errorf("com os 3 campos preenchidos o link deveria aparecer: %q", out)
	}

	for _, faltando := range []string{"guild", "channel", "message"} {
		rec := completo
		switch faltando {
		case "guild":
			rec.DiscordGuildID = ""
		case "channel":
			rec.DiscordChannelID = ""
		case "message":
			rec.DiscordMessageID = ""
		}
		if out := formatEntries([]storage.ScreenshotRecord{rec}); strings.Contains(out, "discord.com/channels") {
			t.Errorf("sem %s o link nao pode ser montado (viraria URL quebrada): %q", faltando, out)
		}
	}
}

func TestFormatEntriesSemDataESemResultado(t *testing.T) {
	semData := storage.ScreenshotRecord{GUID: "abc", PlayerName: "Jogador"}
	if out := formatEntries([]storage.ScreenshotRecord{semData}); !strings.Contains(out, "desconhecida") {
		t.Errorf("CapturedAt zero deveria virar \"desconhecida\": %q", out)
	}
	if out := formatEntries(nil); out != "_nenhum resultado nesta página_" {
		t.Errorf("pagina vazia deveria ter texto proprio, veio %q", out)
	}
}
