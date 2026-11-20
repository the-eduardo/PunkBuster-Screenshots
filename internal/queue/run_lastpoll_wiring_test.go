package queue

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"pbss/internal/discord"
	"pbss/internal/source"
)

// epocaAntiga é a sentinela gravada em lastPoll pelo próprio dublê, DEPOIS do
// Store otimista de boot (pipeline.go:72) e ANTES de List() retornar. 1 é
// 01/01/1970 — qualquer renovação pelo Run() sai dela por ordens de magnitude,
// então o teste distingue "o Run gravou" de "o boot gravou" sem depender de
// sleep nem da truncagem em segundos do Unix().
const epocaAntiga = int64(1)

// fakeSourceSentinela marca lastPoll com a época antiga a cada List() e devolve
// o erro configurado. O ponto do dublê é inverter o ônus da prova: em vez de
// conferir se lastPoll "parece recente" (o que o Store de boot já garantiria por
// pollStaleAfter inteiro), o teste exige que o Run() tenha SOBRESCRITO a
// sentinela — ou não, no ramo de erro.
type fakeSourceSentinela struct {
	p       *Pipeline
	listErr error
	lists   int
}

func (f *fakeSourceSentinela) EnsureConnected() error { return nil }
func (f *fakeSourceSentinela) List(string) ([]source.FileInfo, error) {
	f.lists++
	f.p.lastPoll.Store(epocaAntiga)
	return nil, f.listErr
}
func (f *fakeSourceSentinela) Open(string, string) (io.ReadCloser, error) {
	return nil, errors.New("nao deve ser chamado")
}
func (f *fakeSourceSentinela) ModTime(string, string) (time.Time, error) {
	return time.Time{}, errors.New("nao deve ser chamado")
}
func (f *fakeSourceSentinela) Delete(string, string) error {
	return errors.New("nao deve ser chamado")
}
func (f *fakeSourceSentinela) Close() error { return nil }

// pipelineComSentinela monta o Pipeline + dublê já cruzados (o dublê precisa do
// ponteiro do Pipeline pra gravar a sentinela). WaitingTime alto de propósito:
// um ciclo ocioso não deve interferir, e sleepOrDone respeita o ctx — os 30s
// literais do ramo de erro (pipeline.go:90) não atrasam o teste.
func pipelineComSentinela(t *testing.T, listErr error) (*Pipeline, *fakeSourceSentinela) {
	t.Helper()
	p := &Pipeline{
		SFTPFolder:  "pb",
		TempDir:     t.TempDir(),
		WaitingTime: time.Hour,
		Sender:      discord.NewSender(nil, 1),
	}
	src := &fakeSourceSentinela{p: p, listErr: listErr}
	p.Src = src
	return p, src
}

// TestRunNaoRenovaLastPollQuandoListFalha prova a fiação que mantém o dead-man
// switch do Kuma honesto: com a origem sFTP fora do ar, o Run() NÃO pode
// renovar lastPoll, senão PollAlive() segue true para sempre, o
// StartKumaHeartbeat nunca suspende o pulso (kuma.go:52,65) e o alarme de 15min
// decidido pelo Eduardo em 17/08/2026 morre calado.
//
// É também a provocação da falha, num dublê: origem com List() falhando sempre,
// e a prova de que o sinal que alimenta o Kuma para de ser renovado.
func TestRunNaoRenovaLastPollQuandoListFalha(t *testing.T) {
	p, src := pipelineComSentinela(t, errors.New("dial tcp 131.196.199.123:2025: i/o timeout"))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	p.Run(ctx)

	if src.lists < 1 {
		t.Fatalf("teste vazio: Run nao chegou a chamar List() (lists=%d)", src.lists)
	}
	if got := p.lastPoll.Load(); got != epocaAntiga {
		t.Errorf("lastPoll=%d: Run renovou o sinal de vida do poller apesar do List() ter falhado (esperava a sentinela %d)", got, epocaAntiga)
	}
	if p.PollAlive() {
		t.Error("PollAlive deveria ser false com a origem fora do ar: o pulso do Kuma seguiria subindo com o poller morto")
	}
}

// TestRunRenovaLastPollQuandoListOK é o outro lado da mesma fiação: no caminho
// feliz (inclusive List() vazio, a madrugada ociosa do servidor de origem) o
// Run() TEM que renovar lastPoll. Sem isso o Kuma suspenderia o pulso
// WaitingTime+15min depois de todo boot, dando DOWN falso com o bot saudável.
func TestRunRenovaLastPollQuandoListOK(t *testing.T) {
	p, src := pipelineComSentinela(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	p.Run(ctx)

	if src.lists < 1 {
		t.Fatalf("teste vazio: Run nao chegou a chamar List() (lists=%d)", src.lists)
	}
	if got := p.lastPoll.Load(); got == epocaAntiga {
		t.Errorf("lastPoll preso na sentinela %d: Run nao renovou o sinal de vida do poller depois de um List() bem-sucedido", epocaAntiga)
	}
	if !p.PollAlive() {
		t.Error("PollAlive deveria ser true depois de um List() bem-sucedido: o Kuma daria DOWN falso com o poller saudavel")
	}
}
