package source

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Testes do prazo de handshake SSH + init do sFTP em EnsureConnected
// (sftp.go). Diferente de sftp_probe_test.go (que usa NewClientPipe e nunca
// passa por ssh.Dial), estes dois sobem um listener TCP real em
// 127.0.0.1:0 — o unico jeito de exercitar net.DialTimeout/ssh.NewClientConn.

func encurtaHandshake(t *testing.T, d time.Duration) {
	t.Helper()
	old := sftpHandshakeTimeout
	sftpHandshakeTimeout = d
	t.Cleanup(func() { sftpHandshakeTimeout = old })
}

// TestEnsureConnectedNaoPrendeEmServidorMudo cobre o peer que aceita o TCP e
// nunca fala: sem deadline no handshake, EnsureConnected (chamado sob s.mu)
// ficaria pendurado para sempre, e Close() junto.
func TestEnsureConnectedNaoPrendeEmServidorMudo(t *testing.T) {
	encurtaHandshake(t, 200*time.Millisecond)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- conn
		// Mudo de proposito: aceita o TCP e nunca escreve nada.
	}()

	s, err := NewSFTPSource(ln.Addr().String(), "u", "p", "", true)
	if err != nil {
		t.Fatalf("NewSFTPSource: %v", err)
	}
	s.config.Timeout = 2 * time.Second

	done := make(chan error, 1)
	go func() { done <- s.EnsureConnected() }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("EnsureConnected deveria falhar contra um servidor mudo, veio nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("EnsureConnected ficou preso alem do prazo de handshake (deadline nao aplicada)")
	}

	// Prova que s.mu foi solta: Close() tem que retornar rapido.
	closeDone := make(chan struct{})
	go func() { s.Close(); close(closeDone) }()
	select {
	case <-closeDone:
	case <-time.After(1 * time.Second):
		t.Fatal("Close() ficou presa - EnsureConnected nao soltou s.mu")
	}

	select {
	case c := <-accepted:
		c.Close()
	default:
	}
}

// TestEnsureConnectedZeraDeadlineAposHandshake prova a outra ponta: depois do
// handshake concluido, a deadline tem que ser zerada - senao toda conexao
// morre sftpHandshakeTimeout depois do dial, mesmo saudavel.
func TestEnsureConnectedZeraDeadlineAposHandshake(t *testing.T) {
	encurtaHandshake(t, 300*time.Millisecond)

	addr, stop := servidorSFTPReal(t)
	defer stop()

	s, err := NewSFTPSource(addr, "qualquer", "qualquer", "", true)
	if err != nil {
		t.Fatalf("NewSFTPSource: %v", err)
	}
	s.config.Timeout = 2 * time.Second

	if err := s.EnsureConnected(); err != nil {
		t.Fatalf("EnsureConnected falhou contra servidor saudavel: %v", err)
	}
	defer s.Close()

	// Se a deadline do handshake nao foi zerada, a conexao morre sozinha
	// quando o prazo (300ms) se esgota - dormir alem dele e usar a conexao
	// depois flagra isso.
	time.Sleep(600 * time.Millisecond)

	dir := t.TempDir()
	if _, err := s.List(dir); err != nil {
		t.Fatalf("List apos o prazo de handshake deveria funcionar (deadline deveria ter sido zerada), veio: %v", err)
	}
}

// servidorSFTPReal sobe um servidor SSH+sFTP de verdade sobre TCP real, numa
// porta efemera em 127.0.0.1. Aceita qualquer usuario/senha (o teste nao
// exercita autenticacao). Devolve o endereco e uma func para encerrar.
func servidorSFTPReal(t *testing.T) (string, func()) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	signer, err := ssh.NewSignerFromSigner(priv)
	if err != nil {
		t.Fatalf("ssh.NewSignerFromSigner: %v", err)
	}

	config := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			return nil, nil
		},
	}
	config.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go atenderConexaoSFTP(conn, config)
		}
	}()

	return ln.Addr().String(), func() { ln.Close() }
}

func atenderConexaoSFTP(conn net.Conn, config *ssh.ServerConfig) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		conn.Close()
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "unsupported channel type") //nolint:errcheck
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go func() {
			for req := range requests {
				isSubsystem := req.Type == "subsystem" && len(req.Payload) > 4 && string(req.Payload[4:]) == "sftp"
				if req.WantReply {
					req.Reply(isSubsystem, nil) //nolint:errcheck
				}
				if isSubsystem {
					srv, err := sftp.NewServer(channel)
					if err != nil {
						channel.Close()
						return
					}
					srv.Serve() //nolint:errcheck
					channel.Close()
					return
				}
			}
		}()
	}
}

// TestEnsureConnectedNaoPrendeEmSFTPMudo cobre a metade "init do sFTP" do
// prazo: o servidor conclui o handshake SSH e aceita a sessao, mas nunca
// responde ao pedido do subsistema sftp. Sem o deadline cobrindo tambem o
// sftp.NewClient (ou seja, se o prazo fosse zerado logo apos o handshake SSH),
// EnsureConnected ficaria pendurado para sempre sob s.mu. Contraprova positiva:
// TestEnsureConnectedZeraDeadlineAposHandshake mostra que um servidor saudavel
// conecta normalmente com o mesmo prazo.
func TestEnsureConnectedNaoPrendeEmSFTPMudo(t *testing.T) {
	encurtaHandshake(t, 300*time.Millisecond)

	addr, stop := servidorSSHSemSFTP(t)
	defer stop()

	s, err := NewSFTPSource(addr, "qualquer", "qualquer", "", true)
	if err != nil {
		t.Fatalf("NewSFTPSource: %v", err)
	}
	s.config.Timeout = 2 * time.Second

	done := make(chan error, 1)
	go func() { done <- s.EnsureConnected() }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("EnsureConnected deveria falhar quando o subsistema sftp nunca responde, veio nil")
		}
		if !strings.Contains(err.Error(), "sFTP") {
			t.Fatalf("esperava falha no init do sFTP (nao no handshake SSH), veio: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("EnsureConnected ficou preso no init do sFTP (deadline nao cobre sftp.NewClient)")
	}

	closeDone := make(chan struct{})
	go func() { s.Close(); close(closeDone) }()
	select {
	case <-closeDone:
	case <-time.After(1 * time.Second):
		t.Fatal("Close() ficou presa - EnsureConnected nao soltou s.mu")
	}
}

// servidorSSHSemSFTP sobe um servidor SSH real que autentica e aceita canais
// de sessao, mas engole todos os requests (inclusive o subsistema sftp) sem
// nunca responder.
func servidorSSHSemSFTP(t *testing.T) (string, func()) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	signer, err := ssh.NewSignerFromSigner(priv)
	if err != nil {
		t.Fatalf("ssh.NewSignerFromSigner: %v", err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			return nil, nil
		},
	}
	config.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sconn, chans, reqs, err := ssh.NewServerConn(conn, config)
				if err != nil {
					conn.Close()
					return
				}
				defer sconn.Close()
				go ssh.DiscardRequests(reqs)
				for newChannel := range chans {
					channel, requests, err := newChannel.Accept()
					if err != nil {
						continue
					}
					_ = channel
					// Engole sem responder: o WantReply do subsistema fica pendente.
					go func() {
						for range requests {
						}
					}()
				}
			}()
		}
	}()

	return ln.Addr().String(), func() { ln.Close() }
}
