package commands

import (
	"strings"
	"testing"
	"time"

	"pbss/internal/storage"
)

// TestBuildLastEmbedClampaNoLimiteDoDiscord reproduz o pior caso medido no
// banco de produção: nomes longos e os 3 IDs do Discord no tamanho real
// (snowflake de 19 dígitos) somam bem acima de embedDescBudget (~5580 vs.
// 3950). Sem o clamp em formatEntries, a Description ultrapassaria os 4096
// que o Discord aceita e InteractionRespond devolveria HTTP 400 —
// silenciosamente, pela invariante de respondEphemeral. A mutação que
// derruba este teste é remover o "if b.Len()+len(linha) > embedDescBudget"
// (volta ao loop original sem teto).
func TestBuildLastEmbedClampaNoLimiteDoDiscord(t *testing.T) {
	nomeLongo := strings.Repeat("A", 60)
	regs := make([]storage.ScreenshotRecord, 0, 20)
	for i := 0; i < 20; i++ {
		regs = append(regs, storage.ScreenshotRecord{
			GUID:             "5416a6f4ea15c7a4782f4bf64dab0182",
			PlayerName:       nomeLongo,
			CapturedAt:       time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC),
			DiscordGuildID:   "1185572087415451688",
			DiscordChannelID: "1185572087415451689",
			DiscordMessageID: "1185572087415451690",
		})
	}

	embed := buildLastEmbed("termo", regs)

	if len(embed.Description) > 4096 {
		t.Fatalf("Description estourou o limite do Discord: %d chars", len(embed.Description))
	}
	if !strings.Contains(embed.Description, "omitido(s) (limite do Discord)") {
		t.Fatalf("esperava o aviso de omissao no fim da Description, veio: %q", embed.Description)
	}
}

// TestBuildLastEmbedCasoComumFicaIdenticoAoFormatoDeHoje é o par obrigatório
// do teste acima: garante que o clamp não muda o caminho comum (poucos
// resultados, bem abaixo do teto). Sem ele, um "fix" degenerado que sempre
// trunca passaria despercebido.
func TestBuildLastEmbedCasoComumFicaIdenticoAoFormatoDeHoje(t *testing.T) {
	regs := []storage.ScreenshotRecord{
		{GUID: "abc123", PlayerName: "JoseToalha", CapturedAt: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)},
		{GUID: "def456", PlayerName: "AUTISTA_PH", CapturedAt: time.Date(2026, 9, 4, 12, 1, 0, 0, time.UTC)},
		{GUID: "ghi789", PlayerName: "Jogador3"},
	}
	// Formato hardcoded (não derivado de formatEntries): trava o layout de
	// hoje, senão um "fix" que sempre trunca passaria comparando a função
	// contra si mesma.
	esperado := "**JoseToalha** (`abc123`) — 2026-09-04 12:00:00\n" +
		"**AUTISTA_PH** (`def456`) — 2026-09-04 12:01:00\n" +
		"**Jogador3** (`ghi789`) — desconhecida\n"

	embed := buildLastEmbed("termo", regs)

	if embed.Description != esperado {
		t.Fatalf("clamp alterou o caso comum:\nesperado=%q\nveio=%q", esperado, embed.Description)
	}
	if strings.Contains(embed.Description, "omitido") {
		t.Fatalf("caso comum nao deveria ter aviso de omissao: %q", embed.Description)
	}
}
