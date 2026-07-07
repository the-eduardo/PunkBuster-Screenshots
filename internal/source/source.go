// Package source abstrai o acesso ao servidor de arquivos (FTP ou sFTP) de onde
// os screenshots do PunkBuster são lidos.
package source

import (
	"io"
	"time"
)

// FileInfo descreve um arquivo listado no diretório remoto.
type FileInfo struct {
	Name    string
	Size    int64
	ModTime time.Time
}

// IsScreenshot indica se o arquivo listado é um candidato a screenshot do PB.
func (f FileInfo) IsScreenshot() bool {
	return f.Size > 1000 && len(f.Name) > 4 && f.Name[len(f.Name)-4:] == ".png"
}

// Source é a abstração comum entre o cliente FTP e sFTP usada pelo pipeline.
// Uma implementação deve reconectar-se sozinha quando a conexão cair (EnsureConnected),
// para que o pipeline não precise recriar a conexão a cada ciclo de polling.
type Source interface {
	// EnsureConnected garante que a conexão está ativa, reconectando se necessário.
	EnsureConnected() error
	// List lista os arquivos do diretório configurado.
	List(dir string) ([]FileInfo, error)
	// Open abre um arquivo remoto para leitura.
	Open(dir, name string) (io.ReadCloser, error)
	// ModTime retorna o horário de modificação/criação do arquivo remoto, quando suportado.
	ModTime(dir, name string) (time.Time, error)
	// Delete remove o arquivo remoto.
	Delete(dir, name string) error
	// Close libera a conexão.
	Close() error
}
