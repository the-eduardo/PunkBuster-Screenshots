package parser

import (
	"strings"
	"testing"
	"unicode/utf8"
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

// TestExtract_LinhaDeBannerNaoViraGUID reproduz o caso medido em produção
// (1554 screenshots com GUID decimal de 6-8 dígitos): quando o header do PB
// vem deslocado, a linha 4 é um banner de servidor, não "GUID Nome" — não
// pode ser aceita como se fosse um jogador.
func TestExtract_LinhaDeBannerNaoViraGUID(t *testing.T) {
	data := fixture("944369 131.196.199.123:25220 !          !DuckDuck Op.Locker.60hp")
	info := Extract(data)
	if !info.Empty {
		t.Fatalf("deveria sinalizar Empty para uma linha de banner, veio GUID=%q nome=%q", info.GUID, info.PlayerName)
	}
	if info.GUID != "" {
		t.Fatalf("GUID deveria vir vazio no caso Empty, veio %q", info.GUID)
	}
}

// TestExtract_RawLineNoHeaderDeBanner prova que a linha crua da linha 4 chega
// até o WARN via Info.RawLine quando o header vem deslocado — sem ela, o
// diagnóstico do WARN "sem GUID" (pipeline.go:224) não distingue banner de
// servidor de linha vazia/arquivo truncado, e o arquivo local já foi apagado
// quando o log é lido.
func TestExtract_RawLineNoHeaderDeBanner(t *testing.T) {
	linha := "944369 131.196.199.123:25220 !          !DuckDuck Op.Locker.60hp" // 64 bytes exatos
	data := fixture(linha)
	info := Extract(data)
	if !info.Empty {
		t.Fatalf("deveria sinalizar Empty para uma linha de banner")
	}
	if info.RawLine != linha {
		t.Fatalf("RawLine incorreto: %q", info.RawLine)
	}
}

// TestExtract_RawLineTruncadaEm64Bytes garante o truncamento: sem ele, uma
// linha de banner mais longa vazaria pro log sem limite.
func TestExtract_RawLineTruncadaEm64Bytes(t *testing.T) {
	linha := strings.Repeat("X", 100) // não casa guidPattern, 100 bytes
	data := fixture(linha)
	info := Extract(data)
	if !info.Empty {
		t.Fatalf("deveria sinalizar Empty para uma linha que não casa o guidPattern")
	}
	if len(info.RawLine) != 64 {
		t.Fatalf("esperava RawLine truncado em 64 bytes, veio %d: %q", len(info.RawLine), info.RawLine)
	}
	if info.RawLine != linha[:64] {
		t.Fatalf("RawLine truncado incorretamente: %q", info.RawLine)
	}
}

// TestExtract_RawLineTruncadaNaoPartCaractereMultibyte é o achado do comitê
// de 12/09/2026 (Dev Sênior + QA, convergentes): um corte cru em bytes pode
// partir um "é" (2 bytes UTF-8) bem na fronteira dos 64 bytes, deixando um
// byte de continuação inválido solto no fim da string. RawLine tem que vir
// SEMPRE UTF-8 válido, mesmo que isso signifique truncar 1 byte antes do
// teto.
func TestExtract_RawLineTruncadaNaoPartCaractereMultibyte(t *testing.T) {
	linha := strings.Repeat("X", 63) + "é" + strings.Repeat("Y", 20) // "é" cruza o byte 64
	data := fixture(linha)
	info := Extract(data)
	if !info.Empty {
		t.Fatalf("deveria sinalizar Empty para uma linha que não casa o guidPattern")
	}
	if !utf8.ValidString(info.RawLine) {
		t.Fatalf("RawLine não é UTF-8 válido: %q (bytes: %v)", info.RawLine, []byte(info.RawLine))
	}
	if info.RawLine != strings.Repeat("X", 63) {
		t.Fatalf("esperava truncar ANTES do caractere multibyte partido, veio %q", info.RawLine)
	}
}

// TestExtract_RawLineVazioQuandoLinhaVazia é o caso-guarda: RawLine não deve
// inventar conteúdo quando a linha 4 já vem vazia.
func TestExtract_RawLineVazioQuandoLinhaVazia(t *testing.T) {
	data := fixture("")
	info := Extract(data)
	if !info.Empty {
		t.Fatalf("deveria sinalizar Empty quando a linha do GUID vem em branco")
	}
	if info.RawLine != "" {
		t.Fatalf("RawLine deveria vir vazio quando a linha 4 é vazia, veio %q", info.RawLine)
	}
}

// TestExtract_RawLineVazioQuandoArquivoTruncado é o segundo caso-guarda:
// arquivo curto demais nem chega a ter uma linha 4 pra extrair.
func TestExtract_RawLineVazioQuandoArquivoTruncado(t *testing.T) {
	data := []byte("BF4\nsvss\n")
	info := Extract(data)
	if !info.Empty {
		t.Fatalf("deveria sinalizar Empty quando o arquivo não tem linhas suficientes")
	}
	if info.RawLine != "" {
		t.Fatalf("RawLine deveria vir vazio quando o arquivo está truncado, veio %q", info.RawLine)
	}
}

// TestExtract_GUIDComAsteriscos fecha a lacuna de que nenhuma fixture usava a
// forma real de produção (o pbsvss grava o GUID como *<32 hex>*, 34 chars) —
// essa lacuna é o que deixou passar o bug do /pbss search corrigido em 01/09.
func TestExtract_GUIDComAsteriscos(t *testing.T) {
	data := fixture("*5416a6f4ea15c7a4782f4bf64dab0182* JoseToalha")
	info := Extract(data)
	if info.Empty {
		t.Fatalf("não deveria sinalizar Empty para um GUID real com asteriscos")
	}
	if info.GUID != "*5416a6f4ea15c7a4782f4bf64dab0182*" {
		t.Fatalf("GUID incorreto: %q", info.GUID)
	}
	if info.PlayerName != "JoseToalha" {
		t.Fatalf("nome incorreto: %q", info.PlayerName)
	}
}
