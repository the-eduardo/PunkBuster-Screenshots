package discord

import (
	"sync"
	"testing"
	"time"
)

// TestRunJobPanicNaoMataORunEContinuaAFila prova que um panic dentro de
// processFn não derruba a goroutine do Run: o job seguinte continua sendo
// processado e Run encerra normalmente quando a fila fecha.
//
// O job que panica propositalmente NÃO tem Done (nil) — essa prova é só sobre
// a fila continuar viva, não sobre a notificação de erro (isso é o que os
// outros dois testes cobrem). Depender de um Done que a mutação em teste pode
// nunca chamar faria este teste TRAVAR em vez de FALHAR; um select com prazo
// aqui resolveria o travamento, mas trocaria "FIACAO VAZIA" por um timeout
// genérico — pior diagnóstico pro mesmo problema.
func TestRunJobPanicNaoMataORunEContinuaAFila(t *testing.T) {
	s := NewSender(nil, 2)
	var mu sync.Mutex
	var processados []string
	s.processFn = func(job SendJob) {
		mu.Lock()
		processados = append(processados, job.FileName)
		mu.Unlock()
		if job.FileName == "panica.png" {
			panic("falha simulada de envio")
		}
		job.Done(SendResult{})
	}

	done2 := make(chan struct{})
	go s.Run()
	s.Enqueue(SendJob{FileName: "panica.png"})
	s.Enqueue(SendJob{FileName: "normal.png", Done: func(SendResult) { close(done2) }})

	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatal("FIACAO VAZIA: o 2o job nunca foi concluido — o panic no 1o travou a fila (Run morreu ou ficou preso)")
	}

	s.Close(2 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(processados) != 2 {
		t.Fatalf("FIACAO VAZIA: esperava 2 jobs processados (o panic no 1o deveria deixar o Run vivo pro 2o), veio %d: %v", len(processados), processados)
	}
	if processados[1] != "normal.png" {
		t.Fatalf("o 2o job deveria ser o normal.png, veio %q — a fila parou de fluir depois do panic", processados[1])
	}
}

// TestRunJobPanicChamaDoneComErro prova que, quando processFn panica SEM
// chamar Done, runJob chama Done por ele com um erro — é esse res.Err != nil
// que queue.Pipeline.onSendResult consome pra preservar o remoto e liberar o
// inFlight (ver comentário em runJob).
func TestRunJobPanicChamaDoneComErro(t *testing.T) {
	s := NewSender(nil, 1)
	s.processFn = func(job SendJob) {
		panic("falha simulada antes de chamar Done")
	}

	var doneCalls int
	var gotErr error
	s.runJob(SendJob{
		FileName: "pb000123.png",
		Done: func(res SendResult) {
			doneCalls++
			gotErr = res.Err
		},
	})

	if doneCalls != 1 {
		t.Fatalf("Done deveria ser chamado exatamente 1 vez pelo recover, foi %d", doneCalls)
	}
	if gotErr == nil {
		t.Fatal("FIACAO VAZIA: Done foi chamado sem erro — o pipeline trataria isso como sucesso e vazaria o inFlight pra sempre")
	}
}

// TestRunJobPanicDepoisDeDoneNaoChamaDoneDuasVezes prova que, se processFn já
// chamou Done(sucesso) e só então panica, o recover NÃO chama Done de novo —
// senão o índice sqlite ganharia uma segunda linha pro mesmo screenshot.
func TestRunJobPanicDepoisDeDoneNaoChamaDoneDuasVezes(t *testing.T) {
	s := NewSender(nil, 1)
	s.processFn = func(job SendJob) {
		job.Done(SendResult{MessageID: "123"})
		panic("falha simulada depois do sucesso já reportado")
	}

	var doneCalls int
	var results []SendResult
	s.runJob(SendJob{
		FileName: "pb000456.png",
		Done: func(res SendResult) {
			doneCalls++
			results = append(results, res)
		},
	})

	if doneCalls != 1 {
		t.Fatalf("Done chamado %d vez(es); esperado exatamente 1 — o sync.Once deveria absorver a 2a chamada do recover", doneCalls)
	}
	if results[0].Err != nil {
		t.Fatalf("a chamada que deveria valer é a do sucesso original (Err nil), veio Err=%v", results[0].Err)
	}
}
