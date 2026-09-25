package source

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jlaffaye/ftp"
)

// Testes do probe de liveness do EnsureConnected (ftp.go), espelhando
// sftp_probe_test.go. Aqui nao da' para usar net.Pipe(): ftp.Dial faz
// tconn.RemoteAddr().(*net.TCPAddr), e o pipeAddr do net.Pipe() nao implementa
// essa interface — a asserção entra em panic. Por isso o dublê e' um servidor
// TCP de verdade em loopback (net.Listen("tcp", "127.0.0.1:0")).

// fakeFTPHandshake conduz o handshake minimo que ftp.Dial()+Login() exigem:
// 220 no connect, USER->331, PASS->230, FEAT->500 (codigo != 211 encerra o
// feat() sem erro e deixa "features" vazio, o que impede o OPTS UTF8 ON de
// ser enviado), TYPE I->200. Devolve o *bufio.Reader ja' posicionado para o
// chamador continuar lendo comandos pos-login (ex.: NOOP).
func fakeFTPHandshake(t *testing.T, conn net.Conn) *bufio.Reader {
	t.Helper()
	r := bufio.NewReader(conn)
	fmt.Fprintf(conn, "220 fake\r\n")
	if _, err := r.ReadString('\n'); err != nil { // USER
		return r
	}
	fmt.Fprintf(conn, "331 need password\r\n")
	if _, err := r.ReadString('\n'); err != nil { // PASS
		return r
	}
	fmt.Fprintf(conn, "230 logged in\r\n")
	if _, err := r.ReadString('\n'); err != nil { // FEAT
		return r
	}
	fmt.Fprintf(conn, "500 no feat\r\n")
	if _, err := r.ReadString('\n'); err != nil { // TYPE I
		return r
	}
	fmt.Fprintf(conn, "200 type set to I\r\n")
	return r
}

// ftpSourceComClienteReal monta um FTPSource cujo client fala com um servidor
// fake real (TCP loopback) que responde "200 ok" a todo NOOP. addr aponta pra
// porta 1 (privilegiada, sem listener) de proposito: se o codigo reconectar,
// o dial falha rapido, e isso e' observavel no teste.
func ftpSourceComClienteReal(t *testing.T) (*FTPSource, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() }) //nolint:errcheck

	connCh := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		connCh <- conn
		r := fakeFTPHandshake(t, conn)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(strings.ToUpper(line), "NOOP"):
				fmt.Fprintf(conn, "200 ok\r\n")
			case strings.HasPrefix(strings.ToUpper(line), "QUIT"):
				fmt.Fprintf(conn, "221 bye\r\n")
				conn.Close() //nolint:errcheck
				return
			}
		}
	}()

	cli, err := ftp.Dial(ln.Addr().String(), ftp.DialWithTimeout(2*time.Second))
	if err != nil {
		t.Fatalf("ftp.Dial: %v", err)
	}
	if err := cli.Login("user", "pass"); err != nil {
		t.Fatalf("cli.Login: %v", err)
	}

	s := &FTPSource{addr: "127.0.0.1:1", client: cli}
	kill := func() {
		conn := <-connCh
		conn.Close() //nolint:errcheck
	}
	return s, kill
}

// ftpSourceComClientePendurado monta um FTPSource cujo "servidor" completa o
// handshake e depois emudece — o retrato do TCP meio-aberto que motivou o
// probe (NoOp() nunca retornaria). io.Copy mantem a goroutine lendo (e
// descartando) ate' o cliente fechar a conexao, o que evita vazamento
// permanente de goroutine quando o teste termina.
func ftpSourceComClientePendurado(t *testing.T) *FTPSource {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() }) //nolint:errcheck

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck
		fakeFTPHandshake(t, conn)
		io.Copy(io.Discard, conn) //nolint:errcheck
	}()

	cli, err := ftp.Dial(ln.Addr().String(), ftp.DialWithTimeout(2*time.Second))
	if err != nil {
		t.Fatalf("ftp.Dial: %v", err)
	}
	if err := cli.Login("user", "pass"); err != nil {
		t.Fatalf("cli.Login: %v", err)
	}

	return &FTPSource{addr: "127.0.0.1:1", client: cli}
}

func encurtaProbeFTP(t *testing.T, d time.Duration) {
	t.Helper()
	old := ftpProbeTimeout
	ftpProbeTimeout = d
	t.Cleanup(func() { ftpProbeTimeout = old })
}

func TestFTPEnsureConnectedProbeSaudavelNaoReconecta(t *testing.T) {
	s, _ := ftpSourceComClienteReal(t)
	antes := s.client

	if err := s.EnsureConnected(); err != nil {
		t.Fatalf("probe saudavel deveria passar sem reconectar, veio erro: %v", err)
	}
	if s.client != antes {
		t.Fatal("probe saudavel descartou a conexao viva e reconectou")
	}
}

func TestFTPEnsureConnectedProbeComErroDescartaEReconecta(t *testing.T) {
	s, kill := ftpSourceComClienteReal(t)
	kill() // servidor morre: NoOp retorna erro rapido, sem pendurar

	err := s.EnsureConnected()
	if err == nil {
		t.Fatal("com o servidor morto, EnsureConnected deveria descartar e tentar reconectar (e falhar no dial)")
	}
	if !strings.Contains(err.Error(), "falha ao conectar ao FTP") {
		t.Fatalf("esperava erro do caminho de reconexao (dial), veio: %v", err)
	}
	if s.client != nil {
		t.Fatal("conexao morta nao foi descartada (client deveria ser nil apos o discard)")
	}
}

func TestFTPEnsureConnectedProbePenduradoRespeitaOPrazo(t *testing.T) {
	encurtaProbeFTP(t, 150*time.Millisecond)
	s := ftpSourceComClientePendurado(t)

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
		t.Fatalf("EnsureConnected levou %v — o prazo de %v nao foi respeitado (conexao meio-aberta travaria o source)", decorrido, ftpProbeTimeout)
	}
	if s.client != nil {
		t.Fatal("conexao pendurada nao foi descartada apos o timeout do probe")
	}
}
