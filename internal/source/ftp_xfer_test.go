package source

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jlaffaye/ftp"
)

// Testes do guard s.xfer (ftp.go): a janela de uma transferência RETR (Open
// -> leitura -> Close, quando o "226 closing data connection" pendente na
// conexão de CONTROLE é consumido) tem que ficar serializada contra qualquer
// outro comando de controle emitido por outra goroutine — aqui, Delete().
//
// Diferente de ftp_probe_test.go (que só fala NOOP/QUIT), este dublê precisa
// completar um ciclo real de RETR: EPSV (o client tenta EPSV antes de PASV,
// e FEAT devolve vazio no handshake, então ele tenta mesmo sem anunciado) +
// abrir uma conexão de dados de verdade + DELE na mesma conexão de controle.

// xferOrder registra, com trava própria, a ordem em que o dublê recebeu/
// enviou os eventos relevantes (RETR, 226-enviado, DELE) — é o que prova que
// o DELE só chega DEPOIS do 226 ser consumido pelo Close() do cliente.
type xferOrder struct {
	mu     sync.Mutex
	events []string
}

func (o *xferOrder) record(ev string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, ev)
}

func (o *xferOrder) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]string, len(o.events))
	copy(out, o.events)
	return out
}

// fakeFTPServerComTransferencia sobe um servidor FTP fake de verdade (TCP
// loopback, controle + dados) que fala o suficiente pra sustentar um RETR
// seguido de um DELE na mesma conexão de controle: handshake conhecido,
// EPSV apontando pro listener de dados, RETR->150 + bytes + fecha o lado do
// servidor + 226, e DELE->250. Devolve o endereço de controle e o xferOrder
// compartilhado.
func fakeFTPServerComTransferencia(t *testing.T, fileContent []byte) (addr string, order *xferOrder) {
	t.Helper()

	dataLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen (dados): %v", err)
	}
	t.Cleanup(func() { dataLn.Close() }) //nolint:errcheck
	_, dataPortStr, err := net.SplitHostPort(dataLn.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	dataPort, err := strconv.Atoi(dataPortStr)
	if err != nil {
		t.Fatalf("porta de dados inválida: %v", err)
	}

	ctrlLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen (controle): %v", err)
	}
	t.Cleanup(func() { ctrlLn.Close() }) //nolint:errcheck

	order = &xferOrder{}

	go func() {
		conn, err := ctrlLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck
		r := fakeFTPHandshake(t, conn)

		// dataConnCh recebe a conexão de dados assim que o cliente discar nela
		// (o jlaffaye abre a conexão de dados ANTES de mandar RETR).
		dataConnCh := make(chan net.Conn, 1)

		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			upper := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(upper, "EPSV"):
				go func() {
					dc, err := dataLn.Accept()
					if err != nil {
						return
					}
					dataConnCh <- dc
				}()
				fmt.Fprintf(conn, "229 Entering Extended Passive Mode (|||%d|)\r\n", dataPort)
			case strings.HasPrefix(upper, "RETR"):
				order.record("RETR")
				dc := <-dataConnCh
				fmt.Fprintf(conn, "150 Opening data connection\r\n")
				dc.Write(fileContent) //nolint:errcheck
				dc.Close()            //nolint:errcheck
				fmt.Fprintf(conn, "226 Closing data connection\r\n")
				order.record("226-enviado")
			case strings.HasPrefix(upper, "DELE"):
				order.record("DELE")
				fmt.Fprintf(conn, "250 file deleted\r\n")
			case strings.HasPrefix(upper, "QUIT"):
				fmt.Fprintf(conn, "221 bye\r\n")
				return
			default:
				fmt.Fprintf(conn, "500 comando desconhecido\r\n")
			}
		}
	}()

	return ctrlLn.Addr().String(), order
}

// TestFTPOpenDeleteSerializadosPelaJanelaDeTransferencia é o teste central
// da proposta de 28/09/2026: Delete() chamado enquanto uma transferência
// aberta por Open() ainda não foi fechada tem que BLOQUEAR até o Close(),
// nunca correr concorrente na mesma conexão de controle.
func TestFTPOpenDeleteSerializadosPelaJanelaDeTransferencia(t *testing.T) {
	content := []byte("conteudo de teste do screenshot")
	addr, order := fakeFTPServerComTransferencia(t, content)

	cli, err := ftp.Dial(addr, ftp.DialWithTimeout(2*time.Second))
	if err != nil {
		t.Fatalf("ftp.Dial: %v", err)
	}
	if err := cli.Login("user", "pass"); err != nil {
		t.Fatalf("cli.Login: %v", err)
	}
	s := &FTPSource{addr: addr, client: cli}

	r, err := s.Open("dir", "a.png")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Dá tempo do dublê escrever os bytes, fechar o lado dele e mandar o 226
	// — sem essa folga o teste correria contra o próprio dublê, e não contra
	// o guard que queremos provar.
	time.Sleep(50 * time.Millisecond)

	deleteDone := make(chan error, 1)
	deleteReturned := make(chan struct{})
	go func() {
		err := s.Delete("dir", "b.png")
		deleteDone <- err
		close(deleteReturned)
	}()

	select {
	case <-deleteReturned:
		t.Fatal("Delete() retornou ANTES do Close() da transferência — a janela não está serializada")
	case <-time.After(100 * time.Millisecond):
		// esperado: Delete ainda bloqueado no guard
	}

	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatalf("io.Copy: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("r.Close(): %v", err)
	}

	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("Delete() deveria ter sucedido após o Close(), veio erro: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Delete() não retornou mesmo depois do Close() da transferência")
	}

	ev := order.snapshot()
	deleIdx, okDele := indexOf(ev, "DELE")
	sentIdx, okSent := indexOf(ev, "226-enviado")
	if !okDele || !okSent {
		t.Fatalf("dublê não registrou os eventos esperados: %v", ev)
	}
	if deleIdx < sentIdx {
		t.Fatalf("DELE chegou ANTES do 226 ser enviado — ordem observada: %v", ev)
	}
}

// TestFTPOpenCloseDuasVezesNaoTravaOGuard prova a idempotência do
// xferCloser: pipeline.go fecha o handle duas vezes de propósito (defer +
// fechamento explícito antes do próximo comando de controle). Um segundo
// Unlock() sem Lock() correspondente entraria em panic se o guard não fosse
// idempotente.
func TestFTPOpenCloseDuasVezesNaoTravaOGuard(t *testing.T) {
	content := []byte("conteudo")
	addr, _ := fakeFTPServerComTransferencia(t, content)

	cli, err := ftp.Dial(addr, ftp.DialWithTimeout(2*time.Second))
	if err != nil {
		t.Fatalf("ftp.Dial: %v", err)
	}
	if err := cli.Login("user", "pass"); err != nil {
		t.Fatalf("cli.Login: %v", err)
	}
	s := &FTPSource{addr: addr, client: cli}

	r, err := s.Open("dir", "a.png")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatalf("io.Copy: %v", err)
	}

	if err := r.Close(); err != nil {
		t.Fatalf("primeiro Close(): %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("segundo Close() (idempotência): %v", err)
	}

	// Se o guard não tivesse sido liberado (ou tivesse sido liberado duas
	// vezes e corrompido o estado do mutex), Delete() aqui travaria ou
	// panicaria.
	done := make(chan error, 1)
	go func() { done <- s.Delete("dir", "b.png") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Delete() após double-close deveria ter sucedido, veio erro: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Delete() travou após double-close — guard não foi liberado corretamente")
	}
}

func indexOf(s []string, v string) (int, bool) {
	for i, x := range s {
		if x == v {
			return i, true
		}
	}
	return -1, false
}
