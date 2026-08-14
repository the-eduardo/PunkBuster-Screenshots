package source

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTPSource implementa Source sobre um servidor SFTP, reconectando sob demanda.
type SFTPSource struct {
	addr   string
	config *ssh.ClientConfig

	mu     sync.Mutex
	sshCli *ssh.Client
	client *sftp.Client
}

// NewSFTPSource monta a origem sFTP. hostKey e' a chave publica esperada do
// servidor; sem ela (e sem insecureHostKey explicito) a construcao falha, para
// que a ausencia de verificacao seja sempre uma escolha declarada e nunca o
// comportamento padrao.
func NewSFTPSource(addr, user, password, hostKey string, insecureHostKey bool) (*SFTPSource, error) {
	callback, err := hostKeyCallback(hostKey, insecureHostKey)
	if err != nil {
		return nil, err
	}
	return &SFTPSource{
		addr: addr,
		config: &ssh.ClientConfig{
			User:            user,
			Auth:            []ssh.AuthMethod{ssh.Password(password)},
			HostKeyCallback: callback,
			Timeout:         15 * time.Second,
		},
	}, nil
}

// hostKeyCallback decide como a identidade do servidor e' verificada.
func hostKeyCallback(hostKey string, insecure bool) (ssh.HostKeyCallback, error) {
	if strings.TrimSpace(hostKey) == "" {
		if insecure {
			// Escolha explicita do operador: aceita qualquer chave. A senha do
			// sFTP fica exposta a quem conseguir se passar pelo servidor.
			return ssh.InsecureIgnoreHostKey(), nil
		}
		return nil, errors.New(
			"SFTP_HOST_KEY nao definida: sem ela a conexao aceitaria qualquer servidor e a senha " +
				"ficaria exposta a um ataque man-in-the-middle. Pegue a chave com " +
				"`ssh-keyscan -p <porta> -t ed25519 <host>` e coloque no .env; " +
				"para ignorar a verificacao conscientemente, defina SFTP_INSECURE_HOST_KEY=true")
	}

	expected, err := parseHostKey(hostKey)
	if err != nil {
		return nil, err
	}
	expectedRaw := expected.Marshal()

	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		if bytes.Equal(key.Marshal(), expectedRaw) {
			return nil
		}
		// Mensagem verbosa de proposito: se a chave do servidor for trocada de
		// forma legitima, o log ja entrega a nova fingerprint pra conferencia.
		return fmt.Errorf(
			"chave do host %s nao confere - conexao abortada; esperada %s, recebida %s "+
				"(troca legitima de chave do servidor ou man-in-the-middle)",
			hostname, ssh.FingerprintSHA256(expected), ssh.FingerprintSHA256(key))
	}, nil
}

// parseHostKey aceita tanto "ssh-ed25519 AAAA..." quanto a linha inteira que o
// ssh-keyscan imprime ("[host]:porta ssh-ed25519 AAAA..."), que e' o formato
// que se copia e cola na pratica.
func parseHostKey(line string) (ssh.PublicKey, error) {
	fields := strings.Fields(line)
	if len(fields) >= 2 && !isKeyType(fields[0]) {
		fields = fields[1:]
	}
	if len(fields) < 2 {
		return nil, fmt.Errorf("SFTP_HOST_KEY malformada: esperado algo como \"ssh-ed25519 AAAA...\", recebido %q", line)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.Join(fields, " ")))
	if err != nil {
		return nil, fmt.Errorf("SFTP_HOST_KEY invalida: %w", err)
	}
	return pub, nil
}

func isKeyType(field string) bool {
	for _, prefix := range []string{"ssh-", "ecdsa-", "sk-ssh-", "sk-ecdsa-", "rsa-sha2-"} {
		if strings.HasPrefix(field, prefix) {
			return true
		}
	}
	return false
}

func (s *SFTPSource) EnsureConnected() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client != nil {
		// Probe com prazo: em TCP meio-aberto o Getwd() nunca retorna, e como
		// ele roda sob s.mu isso travaria o source inteiro, Close() incluso.
		// O canal tem buffer 1 para o send nunca ficar preso mesmo após o timeout.
		done := make(chan error, 1)
		go func() { _, err := s.client.Getwd(); done <- err }()

		select {
		case err := <-done:
			if err == nil {
				return nil
			}
		case <-time.After(10 * time.Second):
			slog.Warn("probe do sFTP não respondeu em 10s, descartando conexão e reconectando",
				"addr", s.addr)
		}
		s.closeLocked()
	}

	sshCli, err := ssh.Dial("tcp", s.addr, s.config)
	if err != nil {
		return fmt.Errorf("falha ao conectar via SSH em %s: %w", s.addr, err)
	}
	client, err := sftp.NewClient(sshCli)
	if err != nil {
		sshCli.Close()
		return fmt.Errorf("falha ao criar cliente sFTP: %w", err)
	}
	s.sshCli = sshCli
	s.client = client
	return nil
}

func (s *SFTPSource) List(dir string) ([]FileInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.client.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out = append(out, FileInfo{Name: e.Name(), Size: e.Size(), ModTime: e.ModTime()})
	}
	return out, nil
}

func (s *SFTPSource) Open(dir, name string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client.Open(dir + "/" + name)
}

func (s *SFTPSource) ModTime(dir, name string) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, err := s.client.Stat(dir + "/" + name)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

func (s *SFTPSource) Delete(dir, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client.Remove(dir + "/" + name)
}

func (s *SFTPSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeLocked()
}

func (s *SFTPSource) closeLocked() error {
	var err error
	if s.client != nil {
		err = s.client.Close()
		s.client = nil
	}
	if s.sshCli != nil {
		s.sshCli.Close()
		s.sshCli = nil
	}
	return err
}
