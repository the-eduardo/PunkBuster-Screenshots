package commands

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"pbss/internal/storage"
)

// TestRunLastQuantidadeClampadaNoTeto é o teste de FIAÇÃO do clamp da option
// "quantidade" do /pbss last (11/11/2026). A option é registrada sem
// MinValue/MaxValue (search.go, pbssCommand) e a descrição promete "padrão 5,
// máx 20" — quem pedia quantidade:50 recebia 5, não 20, porque handleCommand
// juntava os dois casos num só `if qty <= 0 || qty > 20 { qty = 5 }`. Ou seja:
// pedir MAIS entregava MENOS que o próprio default, o oposto do prometido.
//
// O teste exercita o caminho que CHAMA o clamp (HandleInteraction →
// handleCommand → runLast), não uma função pura: o título do embed que sai no
// payload real é a evidência, porque buildLastEmbed usa len(results), e
// len(results) é o limite que lookup recebeu.
//
// Fiação provada por mutação (ver mensagem do commit): juntar os dois ifs de
// volta derruba o caso N=50; trocar `qty = 20` por `qty = 5` derruba o mesmo
// caso; trocar `qty = 20` por `qty = 7` derruba N=50 sem tocar nos outros.
func TestRunLastQuantidadeClampadaNoTeto(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "pbss.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer st.Close()

	// 25 linhas do mesmo jogador: mais que o teto de 20, pra o clamp ser o
	// único fator que decide quantos resultados voltam. GUID e filename
	// distintos porque o nome do arquivo RECICLA em produção (teto de 10000
	// no PunkBuster) e não identifica a linha.
	const jogador = "JoseToalha"
	for n := 0; n < 25; n++ {
		if err := st.RecordScreenshot(storage.ScreenshotRecord{
			GUID:       fmt.Sprintf("*%032x*", n),
			PlayerName: jogador,
			FileName:   fmt.Sprintf("pb%06d.png", n),
			Server:     "srv",
		}); err != nil {
			t.Fatalf("RecordScreenshot %d: %v", n, err)
		}
	}

	casos := []struct {
		nome     string
		pedido   int
		esperado int
	}{
		// O defeito: acima do teto tem que cair em 20 (o máximo prometido),
		// nunca em 5.
		{nome: "acima do teto cai em 20", pedido: 50, esperado: 20},
		{nome: "no teto exato fica 20", pedido: 20, esperado: 20},
		// Prova que o clamp não força 20 pra todo mundo: valor válido passa
		// intacto.
		{nome: "valor valido passa intacto", pedido: 7, esperado: 7},
		// O default se mantém nos dois lados do zero.
		{nome: "zero cai no default", pedido: 0, esperado: 5},
		{nome: "negativo cai no default", pedido: -3, esperado: 5},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			session, tr := sessaoCapturando(t)
			h := NewHandler(st)
			i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
				ID: "1", Token: "tok-fake", Type: discordgo.InteractionApplicationCommand,
				Data: discordgo.ApplicationCommandInteractionData{
					Name: "pbss",
					Options: []*discordgo.ApplicationCommandInteractionDataOption{
						{Name: "last", Options: []*discordgo.ApplicationCommandInteractionDataOption{
							{Name: "termo", Type: discordgo.ApplicationCommandOptionString, Value: jogador},
							// O discordgo decodifica option inteira do JSON
							// como float64; IntValue() converte. Usar int aqui
							// faria IntValue() entrar em panic.
							{Name: "quantidade", Type: discordgo.ApplicationCommandOptionInteger, Value: float64(c.pedido)},
						}},
					},
				},
			}}

			h.HandleInteraction(session, i)

			resp := tr.resposta(t)
			if resp.Data == nil || len(resp.Data.Embeds) != 1 {
				t.Fatalf("esperava 1 embed na resposta do /pbss last, veio %+v", resp.Data)
			}
			querido := fmt.Sprintf("Últimos %d screenshots", c.esperado)
			if got := resp.Data.Embeds[0].Title; !strings.HasPrefix(got, querido) {
				t.Fatalf("quantidade:%d deveria render %d resultados; título do payload real: %q",
					c.pedido, c.esperado, got)
			}
			// Guarda redundante de propósito: o título vem de len(results), mas
			// contar as linhas da descrição prova que o embed carrega mesmo a
			// quantidade anunciada (e que nenhuma foi omitida pelo
			// embedDescBudget nestes nomes curtos).
			if n := strings.Count(resp.Data.Embeds[0].Description, "\n"); n != c.esperado {
				t.Fatalf("quantidade:%d: título diz %d mas a descrição tem %d linhas",
					c.pedido, c.esperado, n)
			}
		})
	}
}

// TestPbssCommandDescricaoDaQuantidade crava o texto REGISTRADO da option
// "quantidade". A descrição passou a ser derivada de lastDefaultQty/lastMaxQty
// pra promessa e clamp não divergirem — o preço é que um descuido ali mudaria
// o comando registrado no Discord em silêncio (o Register faz upsert do /pbss
// em todo boot). Este teste é o freio: o texto é byte-a-byte o que estava
// registrado antes da mudança, e o par de valores é conferido junto.
func TestPbssCommandDescricaoDaQuantidade(t *testing.T) {
	if lastDefaultQty != 5 || lastMaxQty != 20 {
		t.Fatalf("default/máx da quantidade mudaram (%d/%d): confirme o texto registrado no Discord antes de aceitar",
			lastDefaultQty, lastMaxQty)
	}

	cmd := pbssCommand()
	var opt *discordgo.ApplicationCommandOption
	for _, sub := range cmd.Options {
		if sub.Name != "last" {
			continue
		}
		for _, o := range sub.Options {
			if o.Name == "quantidade" {
				opt = o
			}
		}
	}
	if opt == nil {
		t.Fatal("option quantidade ausente no subcomando last")
	}
	const querido = "Quantos mostrar (padrão 5, máx 20)"
	if opt.Description != querido {
		t.Fatalf("descrição registrada mudou:\n  antes: %q\n  agora: %q", querido, opt.Description)
	}
	if opt.Required {
		t.Fatal("quantidade deveria seguir opcional")
	}
}

// TestRunLastSemQuantidadeUsaDefault cobre a omissão da option (ela é
// Required: false): sem ela, sub.Options tem 1 elemento e o default de 5 vale.
func TestRunLastSemQuantidadeUsaDefault(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "pbss.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer st.Close()

	const jogador = "JoseToalha"
	for n := 0; n < 25; n++ {
		if err := st.RecordScreenshot(storage.ScreenshotRecord{
			GUID:       fmt.Sprintf("*%032x*", n),
			PlayerName: jogador,
			FileName:   fmt.Sprintf("pb%06d.png", n),
			Server:     "srv",
		}); err != nil {
			t.Fatalf("RecordScreenshot %d: %v", n, err)
		}
	}

	session, tr := sessaoCapturando(t)
	h := NewHandler(st)
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID: "1", Token: "tok-fake", Type: discordgo.InteractionApplicationCommand,
		Data: discordgo.ApplicationCommandInteractionData{
			Name: "pbss",
			Options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "last", Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{Name: "termo", Type: discordgo.ApplicationCommandOptionString, Value: jogador},
				}},
			},
		},
	}}

	h.HandleInteraction(session, i)

	resp := tr.resposta(t)
	if resp.Data == nil || len(resp.Data.Embeds) != 1 {
		t.Fatalf("esperava 1 embed na resposta do /pbss last, veio %+v", resp.Data)
	}
	if got := resp.Data.Embeds[0].Title; !strings.HasPrefix(got, "Últimos 5 screenshots") {
		t.Fatalf("sem a option quantidade esperava o default de 5; título: %q", got)
	}
}
