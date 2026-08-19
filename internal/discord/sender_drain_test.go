package discord

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestSenderCloseDrenaFilaAntesDeRetornar prova que Close espera Run() esvaziar
// a fila antes de devolver o controle, em vez de só fechar o canal e sair.
func TestSenderCloseDrenaFilaAntesDeRetornar(t *testing.T) {
	s := NewSender(nil, 10)

	var processados atomic.Int32
	s.processFn = func(SendJob) {
		time.Sleep(20 * time.Millisecond)
		processados.Add(1)
	}

	go s.Run()

	const total = 5
	for i := 0; i < total; i++ {
		s.Enqueue(SendJob{})
	}

	s.Close(2 * time.Second)

	if got := processados.Load(); got != total {
		t.Fatalf("Close retornou com %d/%d jobs processados; esperava a fila inteira drenada", got, total)
	}
}

// TestSenderCloseRespeitaPrazo prova que Close não fica pendurado para sempre
// quando o processamento não termina a tempo — ele desiste no prazo informado.
func TestSenderCloseRespeitaPrazo(t *testing.T) {
	s := NewSender(nil, 1)

	liberar := make(chan struct{})
	t.Cleanup(func() { close(liberar) })
	s.processFn = func(SendJob) {
		<-liberar // nunca libera durante o teste: simula job pendurado
	}

	go s.Run()
	s.Enqueue(SendJob{})

	inicio := time.Now()
	s.Close(50 * time.Millisecond)
	decorrido := time.Since(inicio)

	if decorrido > time.Second {
		t.Fatalf("Close levou %s para retornar; deveria ter desistido em ~50ms", decorrido)
	}
}
