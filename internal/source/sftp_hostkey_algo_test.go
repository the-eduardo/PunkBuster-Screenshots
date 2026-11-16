package source

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Testes da negociacao do algoritmo de host key (16/11/2026).
//
// Um servidor OpenSSH padrao oferece mais de uma host key (rsa, ecdsa,
// ed25519) e quem escolhe qual sera' usada e' o CLIENTE, pela sua lista de
// preferencia. Sem HostKeyAlgorithms no ssh.ClientConfig, vale a ordem padrao
// do x/crypto — que NAO comeca pelo ed25519, justamente o tipo que o README e
// a mensagem de erro deste pacote mandam fixar (`ssh-keyscan -t ed25519`).
// Nesse caso o servidor manda a chave de outro tipo, o callback compara com a
// ed25519 esperada, nao bate, e a conexao morre com a mensagem de troca de
// chave / man-in-the-middle: alarme falso, e o poller para.
//
// Os dois primeiros casos sao simetricos de proposito (chave esperada
// ed25519 e chave esperada ecdsa, contra o MESMO servidor que oferece as
// duas). Assim o teste nao depende de saber qual tipo a ordem padrao do
// x/crypto prefere hoje: sem a correcao, um dos dois sempre falha.

// signersDeTeste devolve um signer ed25519 e um ecdsa P-256 novos.
func signersDeTeste(t *testing.T) (ssh.Signer, ssh.Signer) {
	t.Helper()

	_, privEd, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	ed, err := ssh.NewSignerFromSigner(privEd)
	if err != nil {
		t.Fatalf("ssh.NewSignerFromSigner (ed25519): %v", err)
	}

	privEc, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	ec, err := ssh.NewSignerFromSigner(privEc)
	if err != nil {
		t.Fatalf("ssh.NewSignerFromSigner (ecdsa): %v", err)
	}

	return ed, ec
}

// servidorSFTPComChaves sobe o mesmo servidor SSH+sFTP de sftp_handshake_test
// (atenderConexaoSFTP), mas com as host keys passadas pelo teste — uma ou
// varias, como um sshd de verdade.
func servidorSFTPComChaves(t *testing.T, signers ...ssh.Signer) (string, func()) {
	t.Helper()

	config := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			return nil, nil
		},
	}
	for _, s := range signers {
		config.AddHostKey(s)
	}

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

// linhaAuthorized devolve a chave publica no formato que vai no SFTP_HOST_KEY.
func linhaAuthorized(t *testing.T, s ssh.Signer) string {
	t.Helper()
	return string(ssh.MarshalAuthorizedKey(s.PublicKey()))
}

// TestEnsureConnectedNegociaOTipoDaChaveFixada e' o caso central: o servidor
// oferece ed25519 E ecdsa, e a conexao tem que funcionar qualquer que seja o
// tipo que o operador fixou no SFTP_HOST_KEY.
func TestEnsureConnectedNegociaOTipoDaChaveFixada(t *testing.T) {
	ed, ec := signersDeTeste(t)
	addr, stop := servidorSFTPComChaves(t, ed, ec)
	defer stop()

	casos := []struct {
		nome   string
		signer ssh.Signer
	}{
		{nome: "chave fixada ed25519", signer: ed},
		{nome: "chave fixada ecdsa", signer: ec},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			s, err := NewSFTPSource(addr, "u", "p", linhaAuthorized(t, c.signer), false)
			if err != nil {
				t.Fatalf("NewSFTPSource: %v", err)
			}
			defer s.Close()
			s.config.Timeout = 2 * time.Second

			if err := s.EnsureConnected(); err != nil {
				t.Fatalf("o servidor oferece as duas host keys; com %s fixada a conexao deveria fechar, veio: %v",
					c.signer.PublicKey().Type(), err)
			}
			if _, err := s.List(t.TempDir()); err != nil {
				t.Fatalf("List apos conectar deveria funcionar, veio: %v", err)
			}
		})
	}
}

// TestEnsureConnectedRejeitaServidorSemOTipoFixado e' a contraprova: fixar o
// algoritmo nao pode afrouxar a verificacao. O servidor so' tem ecdsa, o
// operador fixou uma ed25519 — a conexao tem que falhar.
func TestEnsureConnectedRejeitaServidorSemOTipoFixado(t *testing.T) {
	ed, ec := signersDeTeste(t)
	addr, stop := servidorSFTPComChaves(t, ec)
	defer stop()

	s, err := NewSFTPSource(addr, "u", "p", linhaAuthorized(t, ed), false)
	if err != nil {
		t.Fatalf("NewSFTPSource: %v", err)
	}
	defer s.Close()
	s.config.Timeout = 2 * time.Second

	if err := s.EnsureConnected(); err == nil {
		t.Fatal("servidor sem a host key fixada deveria ser recusado, veio nil")
	}
}

// TestEnsureConnectedComChaveTrocadaFalha fecha a terceira ponta: mesmo tipo
// de chave, chave DIFERENTE da fixada (o cenario que o callback existe pra
// pegar). Fixar o algoritmo nao pode transformar isso num sucesso.
func TestEnsureConnectedComChaveTrocadaFalha(t *testing.T) {
	ed1, _ := signersDeTeste(t)
	ed2, _ := signersDeTeste(t)
	addr, stop := servidorSFTPComChaves(t, ed1)
	defer stop()

	s, err := NewSFTPSource(addr, "u", "p", linhaAuthorized(t, ed2), false)
	if err != nil {
		t.Fatalf("NewSFTPSource: %v", err)
	}
	defer s.Close()
	s.config.Timeout = 2 * time.Second

	if err := s.EnsureConnected(); err == nil {
		t.Fatal("chave do mesmo tipo mas diferente da fixada deveria ser recusada, veio nil")
	}
}
