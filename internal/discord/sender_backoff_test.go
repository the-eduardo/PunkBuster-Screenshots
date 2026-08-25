package discord

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// caminhoSempreInvalido contem um byte NUL: os.Open devolve EINVAL em qualquer
// SO e com qualquer UID, e EINVAL nao casa com fs.ErrNotExist — entao este
// caminho continua produzindo maxAttempts falhas mesmo depois do fail-fast de
// arquivo-inexistente. Um path apenas ausente daria ENOENT e cairia no
// curto-circuito; um diretorio 0000 vira ENOENT quando o teste roda como root
// (o CI da frota roda em container). Achado do QA na drenagem de 25/08/2026.
const caminhoSempreInvalido = "pb-invalido\x00.png"

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
		LocalPath: caminhoSempreInvalido,
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
	if !strings.Contains(doneErr.Error(), "falhou após") {
		t.Fatalf("FIACAO VAZIA: o job nao passou pelo loop de retry; erro final: %v", doneErr)
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
		LocalPath: caminhoSempreInvalido,
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
	// Sem esta linha o teste sobrevive a remocao do fix desta branch: qualquer
	// curto-circuito em process() (ex.: o fail-fast de fs.ErrNotExist da branch
	// auto/20260825-sender-failfast-notfound) satisfaz doneCalls==1 e doneErr!=nil
	// sem nunca entrar no loop de retry. Achado do QA na drenagem de 25/08/2026.
	if !strings.Contains(doneErr.Error(), "falhou após") {
		t.Fatalf("FIACAO VAZIA: o job nao passou pelo loop de retry; erro final: %v", doneErr)
	}
}
