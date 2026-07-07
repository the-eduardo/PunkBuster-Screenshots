package parser

import (
	"strings"
	"testing"
)

// Fixture baseado no formato real do pbsvss: cabeçalho de texto com o GUID
// (hex 32 chars) + nome do jogador na 5ª linha (índice 4), seguido dos bytes
// binários do PNG.
func fixture(guidLine string) []byte {
	lines := []string{"BF4", "svss", "pedro.fragify.net:2025", "2026-06-09 18:50:49", guidLine}
	header := strings.Join(lines, "\n") + "\n"
	return append([]byte(header), []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}...)
}

func TestExtract_Normal(t *testing.T) {
	data := fixture("5416a6f4ea15c7a4782f4bf64dab0182 JoseToalha")
	info := Extract(data)
	if info.GUID != "5416a6f4ea15c7a4782f4bf64dab0182" {
		t.Fatalf("GUID incorreto: %q", info.GUID)
	}
	if info.PlayerName != "JoseToalha" {
		t.Fatalf("nome incorreto: %q", info.PlayerName)
	}
	if info.Empty {
		t.Fatalf("não deveria sinalizar Empty num cabeçalho normal")
	}
}

func TestExtract_NameWithSpaces(t *testing.T) {
	data := fixture("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa Player Com Espaco")
	info := Extract(data)
	if info.PlayerName != "Player Com Espaco" {
		t.Fatalf("nome incorreto: %q", info.PlayerName)
	}
}

func TestExtract_EmptyLine(t *testing.T) {
	// Bug conhecido do PunkBuster: a linha do GUID às vezes vem em branco.
	data := fixture("")
	info := Extract(data)
	if !info.Empty {
		t.Fatalf("deveria sinalizar Empty quando a linha do GUID vem em branco")
	}
}

func TestExtract_TruncatedFile(t *testing.T) {
	// Arquivo corrompido/truncado antes mesmo de chegar na linha do GUID.
	data := []byte("BF4\nsvss\n")
	info := Extract(data)
	if !info.Empty {
		t.Fatalf("deveria sinalizar Empty quando o arquivo não tem linhas suficientes")
	}
}
