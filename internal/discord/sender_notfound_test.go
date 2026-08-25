package discord

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"
)

// TestSenderProcessArquivoInexistenteFalhaDeImediato prova que process() não
// retenta as maxAttempts vezes (30s de backoff) quando o arquivo local já
// sumiu antes do envio — ele nunca vai reaparecer, e a fila do Sender é
// serial (Run, sender.go:105), então cada tentativa inútil bloqueia todo o
// resto da fila. Chama s.process diretamente (o processFn real de produção,
// montado em NewSender), não um dublê, para exercitar a fiação de verdade.
func TestSenderProcessArquivoInexistenteFalhaDeImediato(t *testing.T) {
	s := &Sender{}
	done := make(chan SendResult, 1)

	go s.process(SendJob{
		LocalPath: filepath.Join(t.TempDir(), "nao-existe.png"),
		FileName:  "nao-existe.png",
		Done:      func(r SendResult) { done <- r },
	})

	select {
	case r := <-done:
		if r.Err == nil || !errors.Is(r.Err, fs.ErrNotExist) {
			t.Fatalf("erro deveria preservar fs.ErrNotExist, veio: %v", r.Err)
		}
		if r.MessageID != "" {
			t.Fatalf("nao deveria existir mensagem enviada: %q", r.MessageID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("process ficou retentando um arquivo inexistente (backoff de ate 30s); deveria falhar de imediato")
	}
}
