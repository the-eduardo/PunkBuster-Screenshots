package source

import (
	"encoding/binary"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Testes do probe de liveness do EnsureConnected (sftp.go). A tecnica veio do
// proprio especialista do enxame (17/08/2026): sftp.NewClientPipe da um client
// REAL sobre io.Pipe, sem rede nem servidor de terceiro. Armadilha documentada:
// Client.Close() faz wg.Wait() — o lado servidor PRECISA fechar o writer dele
// ao ver EOF, senao o closeLocked() do source trava o teste inteiro.

// rwc adapta os dois lados de um par de pipes ao io.ReadWriteCloser que o
// sftp.NewServer exige.
type rwc struct {
	io.Reader
	io.WriteCloser
}

// sourceComClientReal monta um SFTPSource cujo client fala com um servidor de
// verdade (o do proprio pkg/sftp) sobre pipes. Devolve tambem um kill() que
// derruba o lado servidor no meio do teste.
//
// A armadilha do wg.Wait() vale ATE para o servidor real: Serve() retorna no
// EOF mas NAO fecha o writer dele, e o Client.Close() (via closeLocked) ficaria
// eternamente esperando o recv loop — por isso o goroutine fecha srvWr ao sair.
func sourceComClientReal(t *testing.T) (*SFTPSource, func()) {
	t.Helper()
	cliRd, srvWr := io.Pipe()
	srvRd, cliWr := io.Pipe()

	srv, err := sftp.NewServer(rwc{srvRd, srvWr})
	if err != nil {
		t.Fatalf("sftp.NewServer: %v", err)
	}
	go func() {
		srv.Serve() //nolint:errcheck // encerra quando os pipes fecham
		srvWr.Close()
	}()

	cli, err := sftp.NewClientPipe(cliRd, cliWr)
	if err != nil {
		t.Fatalf("sftp.NewClientPipe: %v", err)
	}
	return &SFTPSource{
		addr:   "127.0.0.1:1", // porta 1: qualquer reconexao falha rapido
		config: &ssh.ClientConfig{HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: time.Second},
		client: cli,
	}, func() { srv.Close(); srvWr.Close() } //nolint:errcheck
}

// sourceComClientPendurado monta um SFTPSource cujo "servidor" responde SO ao
// handshake de init e depois emudece — o retrato do TCP meio-aberto que
// motivou o probe (Getwd() nunca retornaria). No EOF do lado cliente ele fecha
// o proprio writer, destravando o wg.Wait() interno do Client.Close().
func sourceComClientPendurado(t *testing.T) *SFTPSource {
	t.Helper()
	cliRd, srvWr := io.Pipe()
	srvRd, cliWr := io.Pipe()

	go func() {
		hdr := make([]byte, 4)
		if _, err := io.ReadFull(srvRd, hdr); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(hdr)
		if _, err := io.CopyN(io.Discard, srvRd, int64(n)); err != nil {
			return
		}
		// SSH_FXP_VERSION 3, sem extensoes: o init do client conclui...
		srvWr.Write([]byte{0, 0, 0, 5, 2, 0, 0, 0, 3}) //nolint:errcheck
		// ...e dai em diante silencio absoluto: engole tudo e, no EOF
		// (client fechou), fecha o writer para o recv loop do client morrer.
		io.Copy(io.Discard, srvRd) //nolint:errcheck
		srvWr.Close()
	}()

	cli, err := sftp.NewClientPipe(cliRd, cliWr)
	if err != nil {
		t.Fatalf("sftp.NewClientPipe (init): %v", err)
	}
	return &SFTPSource{
		addr:   "127.0.0.1:1",
		config: &ssh.ClientConfig{HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: time.Second},
		client: cli,
	}
}

func encurtaProbe(t *testing.T, d time.Duration) {
	t.Helper()
	old := sftpProbeTimeout
	sftpProbeTimeout = d
	t.Cleanup(func() { sftpProbeTimeout = old })
}

func TestEnsureConnectedProbeSaudavelNaoReconecta(t *testing.T) {
	s, _ := sourceComClientReal(t)
	defer s.Close()
	antes := s.client

	if err := s.EnsureConnected(); err != nil {
		t.Fatalf("probe saudavel deveria passar sem reconectar, veio erro: %v", err)
	}
	if s.client != antes {
		t.Fatal("probe saudavel descartou a conexao viva e reconectou")
	}
}

func TestEnsureConnectedProbeComErroDescartaEReconecta(t *testing.T) {
	s, killSrv := sourceComClientReal(t)
	killSrv() // servidor morre: Getwd retorna erro rapido, sem pendurar

	err := s.EnsureConnected()
	if err == nil {
		t.Fatal("com o servidor morto, EnsureConnected deveria descartar e tentar reconectar (e falhar no dial)")
	}
	if !strings.Contains(err.Error(), "falha ao conectar via SSH") {
		t.Fatalf("esperava erro do caminho de reconexao (dial), veio: %v", err)
	}
	if s.client != nil {
		t.Fatal("conexao morta nao foi descartada (client deveria ser nil apos closeLocked)")
	}
}

func TestEnsureConnectedProbePenduradoRespeitaOPrazo(t *testing.T) {
	encurtaProbe(t, 150*time.Millisecond)
	s := sourceComClientPendurado(t)

	inicio := time.Now()
	err := s.EnsureConnected()
	decorrido := time.Since(inicio)

	if err == nil {
		t.Fatal("probe pendurado deveria estourar o prazo, descartar e falhar na reconexao")
	}
	if decorrido < 150*time.Millisecond {
		t.Fatalf("EnsureConnected voltou em %v, ANTES do prazo do probe — o timeout nao foi exercitado", decorrido)
	}
	if decorrido > 5*time.Second {
		t.Fatalf("EnsureConnected levou %v — o prazo de %v nao foi respeitado (conexao meio-aberta travaria o source)", decorrido, sftpProbeTimeout)
	}
	if s.client != nil {
		t.Fatal("conexao pendurada nao foi descartada apos o timeout do probe")
	}
}

// TestEnsureConnectedProbeRaceAgressivo prova o comentario em sftp.go:131-133
// (a leitura de s.client tem que acontecer sob s.mu, capturada em variavel
// local antes do "go", nunca dentro da goroutine do probe). Com o timeout do
// probe encurtado para 1us em vez dos 150ms dos testes acima, a goroutine do
// probe e o closeLocked() do timeout ficam genuinamente proximos no tempo, e
// 2000 iteracoes dao ao scheduler chances suficientes de intercalar os dois.
//
// P5 (drenagem 01/09/2026): com 150ms de folga (como os testes acima) o -race
// NUNCA pega — o probe le s.client cedo demais e ja terminou a leitura muito
// antes do timeout escrever nil, entao mesmo a variante bugada (goroutine lendo
// s.client direto) passa limpo. So com o timeout no minimo pratico (1us) o
// -race flagra de verdade: WARNING: DATA RACE, read em sftp.go:137 (dentro do
// go func do probe) vs write anterior em sftp.go:226 (closeLocked, dentro do
// EnsureConnected que disparou o timeout) — e a corrida real derruba o
// processo com nil pointer dereference em Client.nextID quando o ganhador da
// corrida e' a escrita. Confirmado: 5/5 execucoes RED com a mutacao (goroutine
// lendo s.client direto) e 5/5 execucoes verdes com o codigo original (cli
// capturado sob o lock) — ver relatorio da tarefa P5.
func TestEnsureConnectedProbeRaceAgressivo(t *testing.T) {
	for i := 0; i < 2000; i++ {
		func() {
			encurtaProbe(t, 1*time.Microsecond)
			s := sourceComClientPendurado(t)
			defer s.Close()
			_ = s.EnsureConnected()
		}()
	}
}
