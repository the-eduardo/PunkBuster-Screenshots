// Package parser extrai o PBGUID e o nome do jogador do cabeçalho de texto que o
// PunkBuster prefixa nos arquivos .png de screenshot (svss), antes dos dados binários da imagem.
package parser

import (
	"bytes"
	"regexp"
	"unicode/utf8"
)

// guidLineIndex é a linha (0-based) onde o pbsvss sempre grava "GUID NomeDoJogador"
// no cabeçalho do screenshot. É fixa por como o PunkBuster gera esse arquivo — não
// é um formato que varia entre capturas.
const guidLineIndex = 4

// guidPattern casa o formato real que o pbsvss grava na linha do GUID:
// *<32 hex>* (34 chars) ou hex puro de 32 (só usado pelas fixtures de teste,
// mas também um GUID legítimo). Qualquer outra coisa nessa posição é header
// deslocado — banner de servidor, não jogador — e deve seguir o mesmo caminho
// do GUID ausente.
var guidPattern = regexp.MustCompile(`^(\*[0-9a-fA-F]{32}\*|[0-9a-fA-F]{32})$`)

// Info contém os dados extraídos do cabeçalho do screenshot.
type Info struct {
	GUID       string
	PlayerName string
	// Empty indica que a linha do GUID veio vazia ou o arquivo veio truncado —
	// bug ocasional do próprio PunkBuster, não uma mudança de formato. O
	// screenshot ainda deve ser enviado ao Discord, só sem atribuição de jogador.
	Empty bool
	// RawLine é o conteúdo bruto da linha 4, truncado a 64 bytes, preenchido
	// SÓ quando Empty é true por a linha não casar o guidPattern (header
	// deslocado / banner de servidor). Vazio significa linha 4 vazia ou
	// arquivo com menos de 5 linhas — o arquivo local já foi apagado quando o
	// WARN é lido, então é agora ou nunca pra diagnosticar a causa.
	RawLine string
}

// rawSnippetMaxBytes é o teto de bytes de RawLine — o suficiente pra
// identificar o header sem arriscar carregar dado binário de imagem pro log.
const rawSnippetMaxBytes = 64

// rawSnippet trunca b em até rawSnippetMaxBytes SEM partir um caractere
// UTF-8 multibyte ao meio (achado do comitê de 12/09/2026: um corte cru em
// bytes puros pode deixar bytes de continuação inválidos no fim da string,
// que o encoder de log escapa como \xHH ou U+FFFD — não quebra nada, mas
// suja o diagnóstico à toa). Anda rune a rune a partir do início e para
// antes de qualquer rune que ultrapasse o teto; byte realmente inválido
// (dado binário, não texto) avança 1 byte por vez, igual ao corte cru.
func rawSnippet(b []byte) string {
	if len(b) <= rawSnippetMaxBytes {
		return string(b)
	}
	n := 0
	for n < rawSnippetMaxBytes {
		_, size := utf8.DecodeRune(b[n:])
		if n+size > rawSnippetMaxBytes {
			break
		}
		n += size
	}
	return string(b[:n])
}

// Extract lê a linha fixa do cabeçalho onde o PunkBuster grava "GUID Nome".
func Extract(data []byte) Info {
	lines := bytes.SplitN(data, []byte("\n"), guidLineIndex+2)
	if len(lines) <= guidLineIndex {
		return Info{Empty: true}
	}

	line := bytes.TrimSpace(bytes.TrimRight(lines[guidLineIndex], "\r"))
	if len(line) == 0 {
		return Info{Empty: true}
	}

	parts := bytes.SplitN(line, []byte(" "), 2)
	if !guidPattern.Match(parts[0]) {
		return Info{Empty: true, RawLine: rawSnippet(line)}
	}
	info := Info{GUID: string(parts[0])}
	if len(parts) > 1 {
		info.PlayerName = string(parts[1])
	}
	return info
}
