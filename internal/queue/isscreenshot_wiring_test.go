package queue

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"pbss/internal/source"
)

// fakeSourceRecordsOpen lista um arquivo que não é screenshot (extensão
// errada), um .png pequeno demais e um .png válido — e grava sob mutex todo
// nome que o Open chegou a abrir. Open sempre falha (erro que não é
// fs.ErrNotExist), então o Sender nunca é tocado: não é preciso configurá-lo
// pra este teste, só o filtro de FileInfo.IsScreenshot em pipeline.go:97.
type fakeSourceRecordsOpen struct {
	mu     sync.Mutex
	opened []string
}

func (f *fakeSourceRecordsOpen) EnsureConnected() error { return nil }
func (f *fakeSourceRecordsOpen) List(string) ([]source.FileInfo, error) {
	return []source.FileInfo{
		{Name: "notes.txt", Size: 5000, ModTime: time.Now()},
		{Name: "pb000001.png", Size: 500, ModTime: time.Now()},
		{Name: "pb000002.png", Size: 2000, ModTime: time.Now()},
	}, nil
}
func (f *fakeSourceRecordsOpen) Open(_ string, name string) (io.ReadCloser, error) {
	f.mu.Lock()
	f.opened = append(f.opened, name)
	f.mu.Unlock()
	return nil, errors.New("nao deve enviar neste teste")
}
func (f *fakeSourceRecordsOpen) ModTime(string, string) (time.Time, error) { return time.Now(), nil }
func (f *fakeSourceRecordsOpen) Delete(string, string) error {
	return errors.New("nao deve ser chamado")
}
func (f *fakeSourceRecordsOpen) Close() error { return nil }

func (f *fakeSourceRecordsOpen) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.opened))
	copy(out, f.opened)
	return out
}

// TestRunIgnoraArquivoQueNaoEScreenshot exercita a fiação real (Run ->
// o `if !f.IsScreenshot() { continue }` em pipeline.go:97-99), não a função
// isolada: prova que o arquivo sem extensão .png e o .png abaixo do limite de
// tamanho nunca chegam a ser abertos — só o candidato válido é.
func TestRunIgnoraArquivoQueNaoEScreenshot(t *testing.T) {
	backoffOriginal := pollRetryBackoff
	pollRetryBackoff = 10 * time.Millisecond
	t.Cleanup(func() { pollRetryBackoff = backoffOriginal })

	src := &fakeSourceRecordsOpen{}
	p := &Pipeline{
		SFTPFolder:  "pb",
		TempDir:     t.TempDir(),
		WaitingTime: 10 * time.Millisecond,
		Src:         src,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	p.Run(ctx)

	opened := src.snapshot()
	if len(opened) == 0 {
		t.Fatal("teste vazio: Open nunca foi chamado — o Run nao chegou a processar nenhum arquivo")
	}
	for _, nome := range opened {
		if nome != "pb000002.png" {
			t.Errorf("Open chamado para %q: arquivo que nao e screenshot nao deveria ter sido aberto", nome)
		}
	}
}
