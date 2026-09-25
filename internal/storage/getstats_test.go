package storage

import (
	"testing"
	"time"
)

// Este arquivo fecha um buraco de cobertura medido em 22/09/2026: GetStats é
// chamado em TestRecordAndSearch e TestNomeVazioNaoEntraEmPlayerNames, mas
// nenhum dos dois tem mais de um jogador no top — ordem, LIMIT, desempate por
// nome mais recente e a exclusão do INNER JOIN (GUID sem nome fica fora do
// top10 mas continua contando no total) não eram travados por teste nenhum.
// Isso é pré-requisito pra qualquer reescrita de query (ex. CTE) poder trocar
// a implementação com segurança.

func TestGetStatsOrdenaPorContagemDesc(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()

	mustRecordN(t, s, "aaaa", "Jogador3", now, 3)
	mustRecordN(t, s, "bbbb", "Jogador2", now, 2)
	mustRecordN(t, s, "cccc", "Jogador1", now, 1)

	stats, err := s.GetStats()
	if err != nil {
		t.Fatalf("GetStats falhou: %v", err)
	}
	if len(stats.TopPlayers) != 3 {
		t.Fatalf("esperava 3 entradas no top, veio %d: %+v", len(stats.TopPlayers), stats.TopPlayers)
	}
	if stats.TopPlayers[0].GUID != "aaaa" || stats.TopPlayers[0].Count != 3 {
		t.Fatalf("1º lugar esperado aaaa/3, veio %+v", stats.TopPlayers[0])
	}
	if stats.TopPlayers[1].GUID != "bbbb" || stats.TopPlayers[1].Count != 2 {
		t.Fatalf("2º lugar esperado bbbb/2, veio %+v", stats.TopPlayers[1])
	}
	if stats.TopPlayers[2].GUID != "cccc" || stats.TopPlayers[2].Count != 1 {
		t.Fatalf("3º lugar esperado cccc/1, veio %+v", stats.TopPlayers[2])
	}
}

func TestGetStatsLimita10(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()

	guids := []string{"g1", "g2", "g3", "g4", "g5", "g6", "g7", "g8", "g9", "g10", "g11"}
	for i, guid := range guids {
		count := len(guids) - i // g1=11, g2=10, ..., g11=1 (o menor)
		mustRecordN(t, s, guid, "nome-"+guid, now, count)
	}

	stats, err := s.GetStats()
	if err != nil {
		t.Fatalf("GetStats falhou: %v", err)
	}
	if len(stats.TopPlayers) != 10 {
		t.Fatalf("esperava LIMIT 10, veio %d: %+v", len(stats.TopPlayers), stats.TopPlayers)
	}
	for _, tp := range stats.TopPlayers {
		if tp.GUID == "g11" {
			t.Fatalf("g11 tem a menor contagem (1) e não deveria estar no top10: %+v", stats.TopPlayers)
		}
	}
}

func TestGetStatsUsaNomeMaisRecente(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()

	if err := s.RecordScreenshot(ScreenshotRecord{GUID: "aaaa", PlayerName: "NomeAntigo", ReceivedAt: now, Server: "srv", FileName: "a.png"}); err != nil {
		t.Fatalf("RecordScreenshot(NomeAntigo) falhou: %v", err)
	}
	if err := s.RecordScreenshot(ScreenshotRecord{GUID: "aaaa", PlayerName: "NomeNovo", ReceivedAt: now.Add(time.Minute), Server: "srv", FileName: "b.png"}); err != nil {
		t.Fatalf("RecordScreenshot(NomeNovo) falhou: %v", err)
	}

	stats, err := s.GetStats()
	if err != nil {
		t.Fatalf("GetStats falhou: %v", err)
	}
	if len(stats.TopPlayers) != 1 || stats.TopPlayers[0].Name != "NomeNovo" {
		t.Fatalf("esperava o nome de last_seen mais recente (NomeNovo), veio %+v", stats.TopPlayers)
	}
}

func TestGetStatsExcluiGUIDSemNomeDoTopMasContaNoTotal(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()

	// zzzz nunca gera linha em player_names (PlayerName vazio, screenshots.go:53).
	if err := s.RecordScreenshot(ScreenshotRecord{GUID: "zzzz", PlayerName: "", ReceivedAt: now, Server: "srv", FileName: "z.png"}); err != nil {
		t.Fatalf("RecordScreenshot(zzzz) falhou: %v", err)
	}
	if err := s.RecordScreenshot(ScreenshotRecord{GUID: "aaaa", PlayerName: "Jogador", ReceivedAt: now, Server: "srv", FileName: "a.png"}); err != nil {
		t.Fatalf("RecordScreenshot(aaaa) falhou: %v", err)
	}

	stats, err := s.GetStats()
	if err != nil {
		t.Fatalf("GetStats falhou: %v", err)
	}
	for _, tp := range stats.TopPlayers {
		if tp.GUID == "zzzz" {
			t.Fatalf("zzzz não tem nome em player_names e não deveria estar no top10: %+v", stats.TopPlayers)
		}
	}
	if stats.TotalScreenshots != 2 {
		t.Fatalf("TotalScreenshots deve contar zzzz mesmo fora do top10, esperava 2, veio %d", stats.TotalScreenshots)
	}
}

func mustRecordN(t *testing.T, s *Store, guid, name string, base time.Time, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		err := s.RecordScreenshot(ScreenshotRecord{
			GUID:       guid,
			PlayerName: name,
			ReceivedAt: base,
			Server:     "srv",
			FileName:   guid + "-" + name + "-" + time.Duration(i).String() + ".png",
		})
		if err != nil {
			t.Fatalf("RecordScreenshot(%s #%d) falhou: %v", guid, i, err)
		}
	}
}
