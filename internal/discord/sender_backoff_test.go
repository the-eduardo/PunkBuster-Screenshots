package discord

import (
	"sync"
	"testing"
	"time"
)

// TestProcessNaoDormeAposUltimaTentativa prova que o backoff entre tentativas
// nao e pago depois da ULTIMA. process so toca s.session no ramo de sucesso
// (guildFor) e dentro de send (linha que chama a REST); se os.Open falha antes
// disso, o erro volta sem nunca tocar a sessao — entao um &Sender{} com session
// nil e um LocalPath inexistente produz maxAttempts falhas deterministicas sem
// precisar dublar nada.
func TestProcessNaoDormeAposUltimaTentativa(t *testing.T) {
	var mu sync.Mutex
	var esperas []time.Duration
	old := retryBackoff
	retryBackoff = func(attempt int) time.Duration {
		mu.Lock()
		defer mu.Unlock()
		esperas = append(esperas, time.Duration(attempt))
		return time.Millisecond
	}
	t.Cleanup(func() { retryBackoff = old })

	s := &Sender{}

	var doneErr error
	var doneCalls int
	done := make(chan struct{})
	s.process(SendJob{
		LocalPath: "/caminho/que/nao/existe/pb-inexistente.png",
		Done: func(res SendResult) {
			doneCalls++
			doneErr = res.Err
			close(done)
		},
	})
	<-done

	if doneCalls != 1 {
		t.Fatalf("Done deveria ser chamado exatamente 1 vez, foi %d", doneCalls)
	}
	if doneErr == nil {
		t.Fatal("esperava erro apos esgotar as tentativas, veio nil")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(esperas) != maxAttempts-1 {
		t.Fatalf("o backoff nao deve ser pago depois da ultima tentativa: retryBackoff chamado %d vez(es); esperado %d", len(esperas), maxAttempts-1)
	}
}

// TestSenderRunProcessaJobCondenadoSemPendurarClose exercita a fiacao real
// (Enqueue -> Run -> process, sem trocar processFn) com backoff encurtado, o
// caminho que cmd/bot/main.go:80 de fato usa no shutdown. Cobre que Close
// retorna dentro do prazo e sem o WARN de "nao drenou a fila no prazo".
func TestSenderRunProcessaJobCondenadoSemPendurarClose(t *testing.T) {
	old := retryBackoff
	retryBackoff = func(attempt int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { retryBackoff = old })

	s := NewSender(nil, 1)

	var doneCalls int
	var doneErr error
	done := make(chan struct{})
	go s.Run()
	s.Enqueue(SendJob{
		LocalPath: "/caminho/que/nao/existe/pb-inexistente.png",
		Done: func(res SendResult) {
			doneCalls++
			doneErr = res.Err
			close(done)
		},
	})
	<-done

	s.Close(2 * time.Second)

	if doneCalls != 1 {
		t.Fatalf("Done deveria ser chamado exatamente 1 vez, foi %d", doneCalls)
	}
	if doneErr == nil {
		t.Fatal("esperava erro no job condenado, veio nil")
	}
}
