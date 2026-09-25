package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open falhou: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRecordAndSearch(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	err := s.RecordScreenshot(ScreenshotRecord{
		GUID:             "5416a6f4ea15c7a4782f4bf64dab0182",
		PlayerName:       "JoseToalha",
		FileName:         "pb007647.png",
		CapturedAt:       now,
		ReceivedAt:       now,
		Server:           "pedro.fragify.net:2025",
		DiscordGuildID:   "111",
		DiscordChannelID: "222",
		DiscordMessageID: "333",
	})
	if err != nil {
		t.Fatalf("RecordScreenshot falhou: %v", err)
	}

	byGUID, err := s.SearchByGUID("5416a6f4ea15c7a4782f4bf64dab0182", 10)
	if err != nil {
		t.Fatalf("SearchByGUID falhou: %v", err)
	}
	if len(byGUID) != 1 || byGUID[0].PlayerName != "JoseToalha" {
		t.Fatalf("resultado inesperado por GUID: %+v", byGUID)
	}

	byName, err := s.SearchByName("Toalha", 10)
	if err != nil {
		t.Fatalf("SearchByName falhou: %v", err)
	}
	if len(byName) != 1 || byName[0].DiscordMessageID != "333" {
		t.Fatalf("resultado inesperado por nome: %+v", byName)
	}

	stats, err := s.GetStats()
	if err != nil {
		t.Fatalf("GetStats falhou: %v", err)
	}
	if stats.TotalScreenshots != 1 || stats.TotalPlayers != 1 {
		t.Fatalf("stats inesperado: %+v", stats)
	}
	if len(stats.TopPlayers) != 1 || stats.TopPlayers[0].Name != "JoseToalha" {
		t.Fatalf("top players inesperado: %+v", stats.TopPlayers)
	}
}

// TestNomeVazioNaoEntraEmPlayerNames prova o defeito medido em produção
// (01/09/2026): quando o header do PunkBuster vem sem nome, RecordScreenshot
// ainda gravava esse vazio em player_names com last_seen mais recente, e
// GetStats (que escolhe o nome de last_seen máximo) passava a exibir em
// branco um jogador com nome real conhecido. A linha em screenshots continua
// fiel ao header mesmo assim — só o índice de nomes ignora o vazio.
func TestNomeVazioNaoEntraEmPlayerNames(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()

	if err := s.RecordScreenshot(ScreenshotRecord{GUID: "aaaa", PlayerName: "AUTISTA_PH", ReceivedAt: now, Server: "srv", FileName: "a.png"}); err != nil {
		t.Fatalf("RecordScreenshot (nome real) falhou: %v", err)
	}
	if err := s.RecordScreenshot(ScreenshotRecord{GUID: "aaaa", PlayerName: "", ReceivedAt: now.Add(time.Minute), Server: "srv", FileName: "b.png"}); err != nil {
		t.Fatalf("RecordScreenshot (nome vazio) falhou: %v", err)
	}

	stats, err := s.GetStats()
	if err != nil {
		t.Fatalf("GetStats falhou: %v", err)
	}
	if len(stats.TopPlayers) != 1 || stats.TopPlayers[0].Name != "AUTISTA_PH" {
		t.Fatalf("GetStats deveria continuar mostrando o nome real, veio %+v", stats.TopPlayers)
	}

	// A linha do screenshot em si segue fiel ao header, vazio e tudo.
	byGUID, err := s.SearchByGUID("aaaa", 10)
	if err != nil {
		t.Fatalf("SearchByGUID falhou: %v", err)
	}
	if len(byGUID) != 2 {
		t.Fatalf("esperava as 2 linhas de screenshot (uma com nome vazio), veio %d", len(byGUID))
	}
}

// TestSearchByNameEscapaCuringasDoLIKE prova o defeito medido em produção
// (21/09/2026): sem escapar _ e %, SearchByName mistura jogadores diferentes
// cujo nome só coincide na posição dos curingas do LIKE. "Ju5t___C___" e
// "Ju5t___CHR___" são GUIDs DIFERENTES — buscar pelo primeiro não pode trazer
// o segundo.
func TestSearchByNameEscapaCuringasDoLIKE(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()

	if err := s.RecordScreenshot(ScreenshotRecord{GUID: "guidA", PlayerName: "Ju5t___C___", ReceivedAt: now, Server: "srv", FileName: "a.png"}); err != nil {
		t.Fatalf("RecordScreenshot(guidA) falhou: %v", err)
	}
	if err := s.RecordScreenshot(ScreenshotRecord{GUID: "guidB", PlayerName: "Ju5t___CHR___", ReceivedAt: now, Server: "srv", FileName: "b.png"}); err != nil {
		t.Fatalf("RecordScreenshot(guidB) falhou: %v", err)
	}

	results, err := s.SearchByName("Ju5t___C___", 50)
	if err != nil {
		t.Fatalf("SearchByName falhou: %v", err)
	}
	if len(results) != 1 || results[0].GUID != "guidA" {
		t.Fatalf("esperava so o GUID exato (guidA), veio %+v", results)
	}

	// Caso degenerado sem escape: "_" sozinho casaria QUALQUER nome de 1+
	// caractere. Com escape, "_" vira busca literal por um underscore.
	soUnderscore, err := s.SearchByName("_", 50)
	if err != nil {
		t.Fatalf("SearchByName(_) falhou: %v", err)
	}
	if len(soUnderscore) != 2 {
		t.Fatalf("esperava 2 (os dois nomes tem underscore literal), veio %d: %+v", len(soUnderscore), soUnderscore)
	}
}

// TestGetStatsTop10PreservaSemantica caracteriza o comportamento de GetStats
// (top 10) que não pode mudar numa reescrita de query por performance: nome
// mais recente por GUID, contagem correta, ordem por contagem desc, e GUID
// sem nenhum nome em player_names fora do resultado (não pode virar NULL nem
// entrar como linha vazia). Escrito e validado contra a query ORIGINAL antes
// de qualquer reescrita — se este teste já nascer vermelho, a query nova não
// tem base de comparação.
func TestGetStatsTop10PreservaSemantica(t *testing.T) {
	s := openTestStore(t)
	t0 := time.Now().UTC().Truncate(time.Second)
	t1 := t0.Add(time.Minute)
	t2 := t0.Add(2 * time.Minute)

	// GUID A: 3 screenshots, nome mais recente "novo" (t1 e t2), mais antigo "velho" (t0).
	mustRecord(t, s, ScreenshotRecord{GUID: "A", PlayerName: "velho", ReceivedAt: t0, Server: "srv", FileName: "a1.png"})
	mustRecord(t, s, ScreenshotRecord{GUID: "A", PlayerName: "novo", ReceivedAt: t1, Server: "srv", FileName: "a2.png"})
	mustRecord(t, s, ScreenshotRecord{GUID: "A", PlayerName: "novo", ReceivedAt: t2, Server: "srv", FileName: "a3.png"})

	// GUID B: 2 screenshots sem nome nenhum — o guard de PlayerName=="" pula
	// player_names (screenshots.go:53), então B nunca aparece na tabela de
	// nomes. Se vazar pro top10, entra em 2º lugar (2 > 1 de C) e o teste
	// quebra de forma óbvia.
	mustRecord(t, s, ScreenshotRecord{GUID: "B", PlayerName: "", ReceivedAt: t0, Server: "srv", FileName: "b1.png"})
	mustRecord(t, s, ScreenshotRecord{GUID: "B", PlayerName: "", ReceivedAt: t1, Server: "srv", FileName: "b2.png"})

	// GUID C: 1 screenshot, nome "c".
	mustRecord(t, s, ScreenshotRecord{GUID: "C", PlayerName: "c", ReceivedAt: t0, Server: "srv", FileName: "c1.png"})

	stats, err := s.GetStats()
	if err != nil {
		t.Fatalf("GetStats falhou: %v", err)
	}

	if len(stats.TopPlayers) != 2 {
		t.Fatalf("esperava 2 entradas no top10 (A e C, sem B), veio %d: %+v", len(stats.TopPlayers), stats.TopPlayers)
	}
	if got := stats.TopPlayers[0]; got.GUID != "A" || got.Name != "novo" || got.Count != 3 {
		t.Fatalf("1º lugar esperado {A novo 3}, veio %+v", got)
	}
	if got := stats.TopPlayers[1]; got.GUID != "C" || got.Name != "c" || got.Count != 1 {
		t.Fatalf("2º lugar esperado {C c 1}, veio %+v", got)
	}
	for _, tp := range stats.TopPlayers {
		if tp.GUID == "B" {
			t.Fatalf("GUID B nao tem nome em player_names e nao deveria aparecer no top10: %+v", stats.TopPlayers)
		}
	}
}

func mustRecord(t *testing.T, s *Store, rec ScreenshotRecord) {
	t.Helper()
	if err := s.RecordScreenshot(rec); err != nil {
		t.Fatalf("RecordScreenshot(%+v) falhou: %v", rec, err)
	}
}

func TestSameGUIDMultipleNames(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()

	_ = s.RecordScreenshot(ScreenshotRecord{GUID: "aaaa", PlayerName: "NomeAntigo", ReceivedAt: now, Server: "srv", FileName: "a.png"})
	_ = s.RecordScreenshot(ScreenshotRecord{GUID: "aaaa", PlayerName: "NomeNovo", ReceivedAt: now.Add(time.Minute), Server: "srv", FileName: "b.png"})

	results, err := s.SearchByGUID("aaaa", 10)
	if err != nil {
		t.Fatalf("SearchByGUID falhou: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("esperava 2 screenshots pro mesmo GUID com nomes diferentes, obteve %d", len(results))
	}
}
