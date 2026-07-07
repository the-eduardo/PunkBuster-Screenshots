package source

import (
	"fmt"
	"io"
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

func NewSFTPSource(addr, user, password string) *SFTPSource {
	return &SFTPSource{
		addr: addr,
		config: &ssh.ClientConfig{
			User:            user,
			Auth:            []ssh.AuthMethod{ssh.Password(password)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         15 * time.Second,
		},
	}
}

func (s *SFTPSource) EnsureConnected() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client != nil {
		// Uma chamada leve pra confirmar que a conexão ainda está viva.
		if _, err := s.client.Getwd(); err == nil {
			return nil
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
