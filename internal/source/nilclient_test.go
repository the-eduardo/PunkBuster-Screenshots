package source

import "testing"

// Testes do estado deixado por um EnsureConnected que falhou: closeLocked()
// zera s.client, e se o ssh.Dial/ftp.Dial seguinte tambem falhar, o metodo
// retorna com o client ainda nil. O Sender roda em goroutine independente do
// poller e continua chamando Delete() ao confirmar envios da fila local (que
// nao dependem da origem) — sem a guarda de nil, isso desreferencia o
// receiver dentro do pkg/sftp ou do jlaffaye/ftp e panica, derrubando o
// processo e a fila de jobs em memoria junto.
//
// &SFTPSource{} / &FTPSource{} reproduzem esse estado exatamente: zero-value
// com client nil, sem precisar de rede.

func semPanico(t *testing.T, rotulo string, fn func() error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: panicou em vez de retornar erro: %v", rotulo, r)
		}
	}()
	if err := fn(); err == nil {
		t.Errorf("%s: deveria retornar erro sem conexao", rotulo)
	}
}

func TestSFTPSourceMetodosSemConexaoRetornamErro(t *testing.T) {
	s := &SFTPSource{}

	semPanico(t, "List", func() error {
		_, err := s.List("pb")
		return err
	})
	semPanico(t, "Open", func() error {
		_, err := s.Open("pb", "pb000001.png")
		return err
	})
	semPanico(t, "ModTime", func() error {
		_, err := s.ModTime("pb", "pb000001.png")
		return err
	})
	semPanico(t, "Delete", func() error {
		return s.Delete("pb", "pb000001.png")
	})
}

func TestFTPSourceMetodosSemConexaoRetornamErro(t *testing.T) {
	s := &FTPSource{}

	semPanico(t, "List", func() error {
		_, err := s.List("pb")
		return err
	})
	semPanico(t, "Open", func() error {
		_, err := s.Open("pb", "pb000001.png")
		return err
	})
	semPanico(t, "ModTime", func() error {
		_, err := s.ModTime("pb", "pb000001.png")
		return err
	})
	semPanico(t, "Delete", func() error {
		return s.Delete("pb", "pb000001.png")
	})
}
