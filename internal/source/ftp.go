package source

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/jlaffaye/ftp"
)

// FTPSource implementa Source sobre um servidor FTP, reconectando sob demanda.
type FTPSource struct {
	addr, user, pass string

	mu     sync.Mutex
	client *ftp.ServerConn
}

func NewFTPSource(addr, user, pass string) *FTPSource {
	return &FTPSource{addr: addr, user: user, pass: pass}
}

func (s *FTPSource) EnsureConnected() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client != nil {
		if err := s.client.NoOp(); err == nil {
			return nil
		}
		s.client.Quit()
		s.client = nil
	}

	client, err := ftp.Dial(s.addr, ftp.DialWithTimeout(15*time.Second))
	if err != nil {
		return fmt.Errorf("falha ao conectar ao FTP em %s: %w", s.addr, err)
	}
	if err := client.Login(s.user, s.pass); err != nil {
		client.Quit()
		return fmt.Errorf("falha ao autenticar no FTP: %w", err)
	}
	s.client = client
	return nil
}

func (s *FTPSource) List(dir string) ([]FileInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.client.List(dir)
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		if e.Type == ftp.EntryTypeFolder {
			continue
		}
		out = append(out, FileInfo{Name: e.Name, Size: int64(e.Size), ModTime: e.Time})
	}
	return out, nil
}

func (s *FTPSource) Open(dir, name string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client.Retr(dir + "/" + name)
}

func (s *FTPSource) ModTime(dir, name string) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.client.IsGetTimeSupported() {
		return time.Time{}, fmt.Errorf("servidor FTP não suporta MDTM")
	}
	return s.client.GetTime(dir + "/" + name)
}

func (s *FTPSource) Delete(dir, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client.Delete(dir + "/" + name)
}

func (s *FTPSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil {
		return nil
	}
	err := s.client.Quit()
	s.client = nil
	return err
}
