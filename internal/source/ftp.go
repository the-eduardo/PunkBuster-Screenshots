package source

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/jlaffaye/ftp"
)

// ftpProbeTimeout e' o prazo do probe de liveness em EnsureConnected. Var de
// pacote (e nao literal no select) para os testes poderem encurta-lo. Mesmo
// valor e mesmo motivo do sftpProbeTimeout em sftp.go: DialWithTimeout cobre
// so' o dial, nao existe deadline por comando na conexao de controle.
var ftpProbeTimeout = 10 * time.Second

// errFTPNotConnected e' devolvido quando um metodo de dados e' chamado com
// s.client nil (EnsureConnected falhou e nao reconectou ainda). Ver o
// comentario equivalente em sftp.go: sem esta guarda o Sender, que roda em
// goroutine independente e continua confirmando envios da fila local, causa
// um receiver nil dentro do client de FTP.
var errFTPNotConnected = errors.New("FTP nao conectado")

// FTPSource implementa Source sobre um servidor FTP, reconectando sob demanda.
type FTPSource struct {
	addr, user, pass string

	mu     sync.Mutex
	client *ftp.ServerConn

	// xfer serializa a janela INTEIRA de uma transferencia de dados (Retr ->
	// leitura -> Close, quando o "226 closing data connection" pendente e'
	// consumido) contra qualquer outro comando na conexao de CONTROLE. mu nao
	// cobre essa janela: Open() devolve e libera mu assim que o RETR e' emitido,
	// e a leitura roda na goroutine do poller enquanto Delete() roda na
	// goroutine do Sender (via onSendResult). Sem este guard, um DELE emitido
	// nessa janela le o 226 pendente como se fosse a resposta do DELE e
	// desalinha permanentemente todas as respostas seguintes na conexao de
	// controle — o mesmo perigo que pipeline.go:183 ja documenta pro MDTM,
	// so' que no caminho cross-goroutine, que ordenacao dentro de uma unica
	// goroutine nao resolve.
	xfer sync.Mutex
}

func NewFTPSource(addr, user, pass string) *FTPSource {
	return &FTPSource{addr: addr, user: user, pass: pass}
}

func (s *FTPSource) EnsureConnected() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client != nil {
		// Probe com prazo: sem deadline por comando na conexao de controle, um
		// TCP meio-aberto prende o NoOp() indefinidamente, e como ele roda sob
		// s.mu isso travaria o source inteiro, Close() incluso. Mesmo padrao do
		// sftp.go. cli e' lido aqui, sob s.mu: a goroutine nao pode ler s.client
		// direto, porque o descarte abaixo zera esse campo sem sincronizacao com
		// essa escrita.
		cli := s.client
		done := make(chan error, 1)
		go func() { done <- cli.NoOp() }()

		select {
		case err := <-done:
			if err == nil {
				return nil
			}
		case <-time.After(ftpProbeTimeout):
			slog.Warn("probe do FTP não respondeu no prazo, descartando conexão e reconectando",
				"prazo", ftpProbeTimeout, "addr", s.addr)
		}
		// Descarte sem esperar o Quit(): a conexao ja esta suspeita, e um QUIT
		// sincrono nela poderia bloquear o write e pagar o custo sob s.mu de
		// novo. cli e' a copia local, entao a goroutine desgarrada nao corre
		// com o proximo Dial que vai preencher s.client.
		s.client = nil
		go func() { cli.Quit() }() //nolint:errcheck
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
	if s.client == nil {
		return nil, errFTPNotConnected
	}
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
	s.xfer.Lock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil {
		s.xfer.Unlock()
		return nil, errFTPNotConnected
	}
	resp, err := s.client.Retr(dir + "/" + name)
	if err != nil {
		s.xfer.Unlock()
		return nil, err
	}
	return &xferCloser{ReadCloser: resp, unlock: s.xfer.Unlock}, nil
}

// xferCloser libera o guard s.xfer no PRIMEIRO Close, e so' nesse. Idempotencia
// e' obrigatoria aqui: processFile (pipeline.go) fecha o handle duas vezes de
// proposito (defer + fechamento explicito antes do proximo comando de
// controle) — o Response.Close() do jlaffaye ja' e' idempotente (campo
// interno "closed"), mas sync.Mutex nao e': um segundo Unlock() sem Lock()
// correspondente entra em panic.
type xferCloser struct {
	io.ReadCloser
	once   sync.Once
	unlock func()
}

func (x *xferCloser) Close() error {
	err := x.ReadCloser.Close()
	x.once.Do(x.unlock)
	return err
}

func (s *FTPSource) ModTime(dir, name string) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil {
		return time.Time{}, errFTPNotConnected
	}
	if !s.client.IsGetTimeSupported() {
		return time.Time{}, fmt.Errorf("servidor FTP não suporta MDTM")
	}
	return s.client.GetTime(dir + "/" + name)
}

func (s *FTPSource) Delete(dir, name string) error {
	s.xfer.Lock()
	defer s.xfer.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil {
		return errFTPNotConnected
	}
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
