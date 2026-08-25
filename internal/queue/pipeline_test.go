package queue

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pbss/internal/discord"
	"pbss/internal/parser"
	"pbss/internal/source"
	"pbss/internal/storage"
)

// fakeSource implementa source.Source só o suficiente pro onSendResult: o único
// método exercitado é o Delete, e o que interessa é SE ele foi chamado. Os
// demais retornam erro proposital — se algum dia o onSendResult passar a
// chamá-los, o teste falha alto em vez de passar por engano.
type fakeSource struct {
	deleted   []string
	deleteErr error
}

func (f *fakeSource) EnsureConnected() error { return errors.New("nao deve ser chamado") }
func (f *fakeSource) List(string) ([]source.FileInfo, error) {
	return nil, errors.New("nao deve ser chamado")
}
func (f *fakeSource) Open(string, string) (io.ReadCloser, error) {
	return nil, errors.New("nao deve ser chamado")
}
func (f *fakeSource) ModTime(string, string) (time.Time, error) {
	return time.Time{}, errors.New("nao deve ser chamado")
}
func (f *fakeSource) Delete(dir, name string) error {
	f.deleted = append(f.deleted, filepath.Join(dir, name))
	return f.deleteErr
}
func (f *fakeSource) Close() error { return nil }

func newTestPipeline(t *testing.T, src source.Source) (*Pipeline, *storage.Store) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open falhou: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return &Pipeline{ServerLabel: "servidor-de-teste", Src: src, Store: store}, store
}

// arquivoLocal cria o .png temporário que o onSendResult deve (ou não) apagar.
func arquivoLocal(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("png-falso"), 0o644); err != nil {
		t.Fatalf("nao consegui criar o arquivo local de teste: %v", err)
	}
	return path
}

func TestOnSendResultSucessoGravaEIndexaELimpa(t *testing.T) {
	src := &fakeSource{}
	p, store := newTestPipeline(t, src)
	local := arquivoLocal(t, "pb000001.png")
	capturado := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	p.inFlight.Store("pb000001.png", true)
	p.onSendResult("pb", "pb000001.png", local,
		parser.Info{GUID: "abc123", PlayerName: "Jogador"}, capturado,
		discord.SendResult{GuildID: "g1", ChannelID: "c1", MessageID: "m1"})

	recs, err := store.SearchByGUID("abc123", 10)
	if err != nil {
		t.Fatalf("SearchByGUID falhou: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("esperava 1 linha no indice, veio %d", len(recs))
	}
	if recs[0].DiscordMessageID != "m1" || recs[0].Server != "servidor-de-teste" {
		t.Errorf("linha gravada errada: %+v", recs[0])
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Errorf("arquivo local deveria ter sido apagado, mas ainda existe")
	}
	if len(src.deleted) != 1 {
		t.Errorf("esperava 1 Delete remoto, veio %d (%v)", len(src.deleted), src.deleted)
	}
	if _, ainda := p.inFlight.Load("pb000001.png"); ainda {
		t.Errorf("inFlight deveria ter sido liberado")
	}
}

// Este é o caso que responde à dúvida dos 3 arquivos órfãos da rajada de 503 do
// Discord em 07/08: numa falha definitiva NADA é gravado e o remoto NÃO é
// apagado, então o próximo poll pega o arquivo de novo. Não há órfão permanente.
func TestOnSendResultFalhaPreservaRemotoENaoIndexa(t *testing.T) {
	src := &fakeSource{}
	p, store := newTestPipeline(t, src)
	local := arquivoLocal(t, "pb000002.png")

	p.inFlight.Store("pb000002.png", true)
	p.onSendResult("pb", "pb000002.png", local,
		parser.Info{GUID: "def456", PlayerName: "Jogador"}, time.Now().UTC(),
		discord.SendResult{Err: errors.New("HTTP 503 Service Unavailable, no healthy upstream")})

	recs, err := store.SearchByGUID("def456", 10)
	if err != nil {
		t.Fatalf("SearchByGUID falhou: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("falha de envio nao pode gravar no indice, mas gravou %d linha(s)", len(recs))
	}
	if len(src.deleted) != 0 {
		t.Errorf("falha de envio nao pode apagar o remoto, mas chamou Delete em %v", src.deleted)
	}
	if _, err := os.Stat(local); err != nil {
		t.Errorf("arquivo local deveria continuar existindo pra nova tentativa: %v", err)
	}
	if _, ainda := p.inFlight.Load("pb000002.png"); ainda {
		t.Errorf("inFlight deveria ser liberado mesmo na falha, senao o poller nunca reprocessa")
	}
}

// Índice é atalho de busca, o Discord é o arquivo permanente: se a gravação no
// sqlite falhar depois do envio confirmado, a limpeza segue mesmo assim. O
// comentário em pipeline.go documenta a decisão; este teste a trava.
func TestOnSendResultLimpaMesmoComIndiceFalhando(t *testing.T) {
	src := &fakeSource{}
	p, store := newTestPipeline(t, src)
	local := arquivoLocal(t, "pb000003.png")

	// Fechar o Store faz o RecordScreenshot falhar sem precisar de mock do sqlite.
	if err := store.Close(); err != nil {
		t.Fatalf("Close falhou: %v", err)
	}

	p.onSendResult("pb", "pb000003.png", local,
		parser.Info{GUID: "ghi789"}, time.Now().UTC(),
		discord.SendResult{GuildID: "g1", ChannelID: "c1", MessageID: "m3"})

	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Errorf("arquivo local deveria ter sido apagado mesmo com o indice falhando")
	}
	if len(src.deleted) != 1 {
		t.Errorf("remoto deveria ter sido apagado mesmo com o indice falhando, Delete=%v", src.deleted)
	}
}

// TestPollAliveSemPollNenhum cobre o estado inicial antes do primeiro List():
// lastPoll ainda em zero não pode ser lido como "vivo agora mesmo".
func TestPollAliveSemPollNenhum(t *testing.T) {
	p := &Pipeline{WaitingTime: 20 * time.Second}
	if p.PollAlive() {
		t.Errorf("PollAlive deveria ser false antes de qualquer List() bem-sucedido")
	}
}

// TestPollAlivePollRecente é o caminho feliz: List() acabou de acontecer.
func TestPollAlivePollRecente(t *testing.T) {
	p := &Pipeline{WaitingTime: 20 * time.Second}
	p.lastPoll.Store(time.Now().Unix())
	if !p.PollAlive() {
		t.Errorf("PollAlive deveria ser true logo apos um Store recente")
	}
}

// TestPollAlivePollExpirado prova o limite: WaitingTime+pollStaleGrace
// estourado tem que virar false. É o caso que a prova por mutação (trocar <
// por >) derruba.
func TestPollAlivePollExpirado(t *testing.T) {
	p := &Pipeline{WaitingTime: 20 * time.Second}
	expirado := time.Now().Add(-p.pollStaleAfter() - time.Second)
	p.lastPoll.Store(expirado.Unix())
	if p.PollAlive() {
		t.Errorf("PollAlive deveria ser false com lastPoll alem do prazo (%v)", p.pollStaleAfter())
	}
}

// TestPollAlivePollDentroDoPrazoMasQuaseNoLimite prova que o prazo tem folga
// sobre o proprio ciclo de poll (WaitingTime+pollStaleGrace) — um poll a 30s
// do fim da folga ainda conta como vivo.
func TestPollAlivePollDentroDoPrazoMasQuaseNoLimite(t *testing.T) {
	p := &Pipeline{WaitingTime: 20 * time.Second}
	quaseExpirado := time.Now().Add(-p.pollStaleAfter() + 30*time.Second)
	p.lastPoll.Store(quaseExpirado.Unix())
	if !p.PollAlive() {
		t.Errorf("PollAlive deveria ser true um pouco antes do prazo de %v", p.pollStaleAfter())
	}
}

// TestPollAliveSobreviveUmCicloOciosoCompleto prova o bug real de producao:
// com WAITING_TIME=20 (minutos, config.go:82-86), o poller dorme 20min entre
// List() quando nao ha arquivo novo. Um prazo fixo de 15min (o que estava em
// producao ate 20/08/2026) julgava esse silencio normal como poller morto
// ANTES do proximo ciclo terminar — falso alarme em todo ciclo ocioso. Este
// teste falha contra um pollStaleAfter fixo de 15min independente de
// WaitingTime (o comportamento anterior a esta correção).
func TestPollAliveSobreviveUmCicloOciosoCompleto(t *testing.T) {
	p := &Pipeline{WaitingTime: 20 * time.Minute}
	p.lastPoll.Store(time.Now().Add(-21 * time.Minute).Unix())
	if !p.PollAlive() {
		t.Errorf("PollAlive deveria ser true 21min apos o ultimo poll com WaitingTime=20min (um ciclo ocioso completo)")
	}
}

// TestPollAliveMorreAlemDaFolga prova o outro lado do dead-man switch: passado
// WaitingTime+pollStaleGrace, o poller e' mesmo considerado morto — a
// recalibracao alarga o prazo, mas nao desliga o alarme.
func TestPollAliveMorreAlemDaFolga(t *testing.T) {
	p := &Pipeline{WaitingTime: 20 * time.Minute}
	p.lastPoll.Store(time.Now().Add(-(20*time.Minute + pollStaleGrace + time.Minute)).Unix())
	if p.PollAlive() {
		t.Errorf("PollAlive deveria ser false alem de WaitingTime+pollStaleGrace")
	}
}

// trackedReadCloser simula o handle de dados de uma conexão FTP: fica "aberto"
// até Close() ser chamado, exatamente como o Response do jlaffaye/ftp que
// consome o 226 pendente na conexão de controle só no seu Close.
type trackedReadCloser struct {
	r      io.Reader
	aberto *bool
}

func (t *trackedReadCloser) Read(p []byte) (int, error) { return t.r.Read(p) }
func (t *trackedReadCloser) Close() error                { *t.aberto = false; return nil }

// fakeSourceFTP denuncia (via t.Errorf) qualquer comando emitido na conexão de
// controle simulada (ModTime, Delete) enquanto o handle de dados do Open ainda
// está aberto — o sintoma exato da desincronização de protocolo do FTP descrita
// na proposta de 23/08.
type fakeSourceFTP struct {
	t      *testing.T
	aberto bool

	modTimeCalls int
	deleteCalls  int
}

func (f *fakeSourceFTP) EnsureConnected() error { return nil }
func (f *fakeSourceFTP) List(string) ([]source.FileInfo, error) {
	return nil, errors.New("nao deve ser chamado")
}
func (f *fakeSourceFTP) Open(dir, name string) (io.ReadCloser, error) {
	f.aberto = true
	return &trackedReadCloser{r: strings.NewReader("png-falso-conteudo"), aberto: &f.aberto}, nil
}
func (f *fakeSourceFTP) ModTime(dir, name string) (time.Time, error) {
	f.modTimeCalls++
	if f.aberto {
		f.t.Errorf("ModTime chamado com a conexao de dados ainda aberta")
	}
	return time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC), nil
}
func (f *fakeSourceFTP) Delete(dir, name string) error {
	f.deleteCalls++
	if f.aberto {
		f.t.Errorf("Delete chamado com a conexao de dados ainda aberta")
	}
	return nil
}
func (f *fakeSourceFTP) Close() error { return nil }

// TestProcessFileNaoEmiteComandoComHandleAberto prova a correção de 23/08: no
// FTP, MDTM (e, por extensão, DELE em onSendResult) não podem ser emitidos na
// conexão de controle enquanto o handle de dados do RETR (Open) segue aberto,
// senão a resposta 226 pendente do RETR é lida como se fosse a resposta do
// comando seguinte, desalinhando todas as respostas dali em diante. Exercita
// processFile de ponta a ponta — o mesmo método que Run() chama a cada arquivo
// listado (pipeline.go:102) — e não uma função isolada.
//
// Sender: NewSender(nil, 1) é seguro aqui porque Enqueue só empurra pro canal
// interno (mesmo raciocínio já registrado para sender.go:148: session só é
// tocado dentro de process(), que Run() nunca chega a rodar neste teste).
func TestProcessFileNaoEmiteComandoComHandleAberto(t *testing.T) {
	src := &fakeSourceFTP{t: t}
	p := &Pipeline{
		ServerLabel: "servidor-de-teste",
		SFTPFolder:  "pb",
		TempDir:     t.TempDir(),
		Src:         src,
		Sender:      discord.NewSender(nil, 1),
	}

	p.processFile(source.FileInfo{Name: "pb000001.png", Size: 2000, ModTime: time.Now()})

	if src.modTimeCalls != 1 {
		t.Errorf("esperava 1 chamada a ModTime, veio %d", src.modTimeCalls)
	}
	if src.aberto {
		t.Errorf("handle remoto deveria estar fechado apos processFile retornar")
	}
}
