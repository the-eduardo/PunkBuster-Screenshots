// Package parser extrai o PBGUID e o nome do jogador do cabeçalho de texto que o
// PunkBuster prefixa nos arquivos .png de screenshot (svss), antes dos dados binários da imagem.
package parser

import "bytes"

// guidLineIndex é a linha (0-based) onde o pbsvss sempre grava "GUID NomeDoJogador"
// no cabeçalho do screenshot. É fixa por como o PunkBuster gera esse arquivo — não
// é um formato que varia entre capturas.
const guidLineIndex = 4

// Info contém os dados extraídos do cabeçalho do screenshot.
type Info struct {
	GUID       string
	PlayerName string
	// Empty indica que a linha do GUID veio vazia ou o arquivo veio truncado —
	// bug ocasional do próprio PunkBuster, não uma mudança de formato. O
	// screenshot ainda deve ser enviado ao Discord, só sem atribuição de jogador.
	Empty bool
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
	info := Info{GUID: string(parts[0])}
	if len(parts) > 1 {
		info.PlayerName = string(parts[1])
	}
	return info
}
