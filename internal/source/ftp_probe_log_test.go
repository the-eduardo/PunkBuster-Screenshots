package source

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer serializa escrita/leitura do log capturado: a goroutine do
// Quit() desgarrado continua viva depois do EnsureConnected voltar.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestFTPEnsureConnectedProbePenduradoLogaWarn prova a fiação do WARN do
// ramo de timeout do probe (drenagem 25/09/2026): sem ele, apagar o
// slog.Warn passava com a suíte verde, e o descarte de conexão por probe
// pendurado sumiria do log sem nenhum sinal. Par positivo/negativo no mesmo
// teste: o probe saudável NÃO pode emitir o WARN.
func TestFTPEnsureConnectedProbePenduradoLogaWarn(t *testing.T) {
	buf := &lockedBuffer{}
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(orig) })

	const marca = "probe do FTP não respondeu no prazo"

	saudavel, _ := ftpSourceComClienteReal(t)
	if err := saudavel.EnsureConnected(); err != nil {
		t.Fatalf("probe saudavel nao deveria falhar: %v", err)
	}
	if strings.Contains(buf.String(), marca) {
		t.Fatalf("probe saudavel nao deveria logar o WARN de timeout, log: %q", buf.String())
	}

	encurtaProbeFTP(t, 150*time.Millisecond)
	pendurado := ftpSourceComClientePendurado(t)
	if err := pendurado.EnsureConnected(); err == nil {
		t.Fatal("probe pendurado deveria estourar o prazo e falhar na reconexao")
	}
	if n := strings.Count(buf.String(), marca); n != 1 {
		t.Fatalf("esperava exatamente 1 WARN de timeout do probe, veio %d, log: %q", n, buf.String())
	}
}
