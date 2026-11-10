package queue

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"pbss/internal/discord"
	"pbss/internal/source"
)

// fakeSourceSempreMesmo lista sempre o mesmo screenshot e nunca falha o Open —
// o cenário em que o dedupe de inFlight (pipeline.go:100-102) é a única coisa
// que impede o poller de rebaixar/reprocessar um arquivo que ainda está "em
// voo" (baixado, enfileirado, aguardando confirmação do Sender).
type fakeSourceSempreMesmo struct {
	mu    sync.Mutex
	lists int
	opens int
}

func (f *fakeSourceSempreMesmo) EnsureConnected() error { return nil }
func (f *fakeSourceSempreMesmo) List(string) ([]source.FileInfo, error) {
	f.mu.Lock()
	f.lists++
	f.mu.Unlock()
	return []source.FileInfo{{Name: "pb000001.png", Size: 2000, ModTime: time.Now()}}, nil
}
func (f *fakeSourceSempreMesmo) Open(string, string) (io.ReadCloser, error) {
	f.mu.Lock()
	f.opens++
	f.mu.Unlock()
	return io.NopCloser(bytes.NewReader(cabecalhoNormal())), nil
}
func (f *fakeSourceSempreMesmo) ModTime(string, string) (time.Time, error) { return time.Now(), nil }
func (f *fakeSourceSempreMesmo) Delete(string, string) error {
	return errors.New("nao deve ser chamado")
}
func (f *fakeSourceSempreMesmo) Close() error { return nil }

func (f *fakeSourceSempreMesmo) snapshot() (lists, opens int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists, f.opens
}

// TestRunNaoReprocessaArquivoEmVoo exercita a fiação real (Run -> processFile
// -> o guard de inFlight.LoadOrStore em pipeline.go:100-102), não a struct
// isolada: usa um Sender SEM Run() consumindo a fila, então o job enfileirado
// em processFile fica parado no buffer e onSendResult nunca é chamado — é
// exatamente o estado "em voo" em que o Run() volta a listar logo depois
// (pending>0, pipeline.go:113).
func TestRunNaoReprocessaArquivoEmVoo(t *testing.T) {
	src := &fakeSourceSempreMesmo{}
	p := &Pipeline{
		SFTPFolder:  "pb",
		TempDir:     t.TempDir(),
		WaitingTime: 50 * time.Millisecond,
		Src:         src,
		Sender:      discord.NewSender(nil, 2), // sem Run(): job fica em voo
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	fim := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(fim)
	}()

	select {
	case <-fim:
	case <-time.After(2 * time.Second):
		t.Error("Run travou em Enqueue: reenfileirou o mesmo arquivo ate encher a fila (dedupe inFlight quebrado)")
	}

	lists, opens := src.snapshot()
	if lists < 2 {
		t.Fatalf("teste vazio: Run listou %d vez(es), precisa relistar com o arquivo em voo", lists)
	}
	if opens != 1 {
		t.Errorf("Open chamado %d vezes: arquivo em voo foi baixado de novo (dedupe inFlight quebrado)", opens)
	}
}
