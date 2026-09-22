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
