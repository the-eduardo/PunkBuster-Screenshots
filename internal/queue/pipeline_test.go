package queue

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// TestOnSendResultComSourceDesconectadaNaoPanica exercita a fiação real, não
// uma função isolada: p.Src.Delete em pipeline.go:228 é chamado com o tipo
// concreto SFTPSource (não um dublê), no mesmo estado que um EnsureConnected
// que falhou deixa (client nil) — o Sender confirma o envio numa goroutine
// independente do poller, sem esperar a origem reconectar. Antes da guarda de
// nil em internal/source/sftp.go isso panicava dentro do pkg/sftp e derrubava
// o processo; a prova por mutação é remover a guarda e ver este teste falhar.
func TestOnSendResultComSourceDesconectadaNaoPanica(t *testing.T) {
	src := &source.SFTPSource{}
	p, store := newTestPipeline(t, src)
	local := arquivoLocal(t, "pb000004.png")

	p.inFlight.Store("pb000004.png", true)
	p.onSendResult("pb", "pb000004.png", local,
		parser.Info{GUID: "jkl012", PlayerName: "Jogador"}, time.Now().UTC(),
		discord.SendResult{GuildID: "g1", ChannelID: "c1", MessageID: "m4"})

	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Errorf("arquivo local deveria ter sido apagado mesmo com Delete remoto falhando")
	}
	recs, err := store.SearchByGUID("jkl012", 10)
	if err != nil {
		t.Fatalf("SearchByGUID falhou: %v", err)
	}
	if len(recs) != 1 {
		t.Errorf("esperava 1 linha no indice (o envio em si foi confirmado), veio %d", len(recs))
	}
	if _, ainda := p.inFlight.Load("pb000004.png"); ainda {
		t.Errorf("inFlight deveria ter sido liberado")
	}
}

// TestOnSendResultComFTPDesconectadaNaoPanica é o mesmo caso para o FTPSource
// concreto (caminho de código separado em internal/source/ftp.go).
func TestOnSendResultComFTPDesconectadaNaoPanica(t *testing.T) {
	src := &source.FTPSource{}
	p, store := newTestPipeline(t, src)
	local := arquivoLocal(t, "pb000005.png")

	p.inFlight.Store("pb000005.png", true)
	p.onSendResult("pb", "pb000005.png", local,
		parser.Info{GUID: "mno345", PlayerName: "Jogador"}, time.Now().UTC(),
		discord.SendResult{GuildID: "g1", ChannelID: "c1", MessageID: "m5"})

	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Errorf("arquivo local deveria ter sido apagado mesmo com Delete remoto falhando")
	}
	recs, err := store.SearchByGUID("mno345", 10)
	if err != nil {
		t.Fatalf("SearchByGUID falhou: %v", err)
	}
	if len(recs) != 1 {
		t.Errorf("esperava 1 linha no indice (o envio em si foi confirmado), veio %d", len(recs))
	}
	if _, ainda := p.inFlight.Load("pb000005.png"); ainda {
		t.Errorf("inFlight deveria ter sido liberado")
	}
}

// TestOnSendResultNomeVazioNaoSequestraNomeDoJogador exercita a fiação real
// (onSendResult, o método que Enqueue chama via Done — pipeline.go:212), não
// RecordScreenshot isolada: reproduz o cenário exato medido em produção
// (01/09/2026) de um GUID cujo primeiro envio tem nome real e o segundo (um
// header do PunkBuster sem nome) chega depois. Sem o guard em
// screenshots.go, o /pbss stats passaria a exibir esse jogador em branco.
func TestOnSendResultNomeVazioNaoSequestraNomeDoJogador(t *testing.T) {
	src := &fakeSource{}
	p, store := newTestPipeline(t, src)

	comNome := arquivoLocal(t, "pb000010.png")
	p.inFlight.Store("pb000010.png", true)
	p.onSendResult("pb", "pb000010.png", comNome,
		parser.Info{GUID: "aaaa", PlayerName: "AUTISTA_PH"}, time.Now().UTC(),
		discord.SendResult{GuildID: "g1", ChannelID: "c1", MessageID: "m1"})

	semNome := arquivoLocal(t, "pb000011.png")
	p.inFlight.Store("pb000011.png", true)
	p.onSendResult("pb", "pb000011.png", semNome,
		parser.Info{GUID: "aaaa", PlayerName: ""}, time.Now().UTC(),
		discord.SendResult{GuildID: "g1", ChannelID: "c1", MessageID: "m2"})

	stats, err := store.GetStats()
	if err != nil {
		t.Fatalf("GetStats falhou: %v", err)
	}
	if len(stats.TopPlayers) != 1 || stats.TopPlayers[0].Name != "AUTISTA_PH" {
		t.Errorf("o nome vazio do segundo envio nao pode sequestrar o nome real, TopPlayers=%+v", stats.TopPlayers)
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

// TestSweepTempDirRetencaoZeroPreservaArquivoEmVoo prova o defeito descrito na
// proposta do enxame de 27/08: Pipeline é struct pública sem validação própria
// (newTestPipeline monta um &Pipeline{} com RetentionHours zerado), e maxAge=0
// faria o janitor apagar todo arquivo do TempDir a cada tick — inclusive um
// baixado agora mesmo, ainda em voo aguardando confirmação de envio. A prova
// por mutação é remover o "if p.RetentionHours <= 0" de retentionMaxAge (volta
// ao código de produção anterior a esta correção): maxAge vira 0 e o arquivo
// recém-criado passa a ser removido, derrubando este teste.
func TestSweepTempDirRetencaoZeroPreservaArquivoEmVoo(t *testing.T) {
	dir := t.TempDir()
	p := &Pipeline{TempDir: dir, RetentionHours: 0}
	path := filepath.Join(dir, "pb999001.png")
	if err := os.WriteFile(path, []byte("em-voo"), 0o644); err != nil {
		t.Fatalf("nao consegui criar o arquivo de teste: %v", err)
	}

	removed := p.sweepTempDir()

	if removed != 0 {
		t.Errorf("esperava 0 arquivos removidos com RetentionHours=0, removeu %d", removed)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("arquivo em voo nao deveria ter sido apagado: %v", err)
	}
}

// TestSweepTempDirRemoveExpirado é o par obrigatório do teste anterior: sem
// ele, um "fix" degenerado que nunca apaga nada (ex.: sempre usar 24h)
// passaria sozinho. A mutação que o derruba é fazer retentionMaxAge ignorar
// RetentionHours e sempre devolver 24h — o arquivo de 2h atrás com
// RetentionHours=1 sobreviveria e este teste falharia.
func TestSweepTempDirRemoveExpirado(t *testing.T) {
	dir := t.TempDir()
	p := &Pipeline{TempDir: dir, RetentionHours: 1}
	path := filepath.Join(dir, "pb999002.png")
	if err := os.WriteFile(path, []byte("expirado"), 0o644); err != nil {
		t.Fatalf("nao consegui criar o arquivo de teste: %v", err)
	}
	velho := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, velho, velho); err != nil {
		t.Fatalf("nao consegui ajustar o mtime do arquivo de teste: %v", err)
	}

	removed := p.sweepTempDir()

	if removed != 1 {
		t.Errorf("esperava 1 arquivo removido, removeu %d", removed)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("arquivo expirado deveria ter sido apagado")
	}
}

// TestRunJanitorUsaRetencaoConfigurada exercita a fiação real (runJanitor),
// não sweepTempDir isolada: se alguém remover a chamada a sweepTempDir de
// dentro de runJanitor, os dois testes acima continuam verdes e o janitor de
// produção fica inerte. A mutação que derruba este teste é exatamente essa —
// fazer runJanitor ignorar sweepTempDir() no case do ticker.
func TestRunJanitorUsaRetencaoConfigurada(t *testing.T) {
	intervaloOriginal := janitorInterval
	janitorInterval = 10 * time.Millisecond
	t.Cleanup(func() { janitorInterval = intervaloOriginal })

	dir := t.TempDir()
	p := &Pipeline{TempDir: dir, RetentionHours: 1}
	path := filepath.Join(dir, "pb999003.png")
	if err := os.WriteFile(path, []byte("expirado"), 0o644); err != nil {
		t.Fatalf("nao consegui criar o arquivo de teste: %v", err)
	}
	velho := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, velho, velho); err != nil {
		t.Fatalf("nao consegui ajustar o mtime do arquivo de teste: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.runJanitor(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("runJanitor nao removeu o arquivo expirado dentro do prazo")
}

// fakeSourceAlwaysFailsOpen simula uma origem viva (List sempre acha o mesmo
// arquivo) cujo download sempre falha (Open sempre erro) — o cenário exato do
// busy-loop: pending++ incondicional fazia Run() voltar ao topo do loop sem
// dormir, martelando List() sem sleep nenhum.
type fakeSourceAlwaysFailsOpen struct {
	mu        sync.Mutex
	listCalls int
}

func (f *fakeSourceAlwaysFailsOpen) EnsureConnected() error { return nil }
func (f *fakeSourceAlwaysFailsOpen) List(string) ([]source.FileInfo, error) {
	f.mu.Lock()
	f.listCalls++
	f.mu.Unlock()
	return []source.FileInfo{{Name: "pb000001.png", Size: 2000, ModTime: time.Now()}}, nil
}
func (f *fakeSourceAlwaysFailsOpen) Open(string, string) (io.ReadCloser, error) {
	return nil, errors.New("download sempre falha neste teste")
}
func (f *fakeSourceAlwaysFailsOpen) ModTime(string, string) (time.Time, error) {
	return time.Time{}, errors.New("nao deve ser chamado (Open falha antes)")
}
func (f *fakeSourceAlwaysFailsOpen) Delete(string, string) error {
	return errors.New("nao deve ser chamado")
}
func (f *fakeSourceAlwaysFailsOpen) Close() error { return nil }

func (f *fakeSourceAlwaysFailsOpen) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listCalls
}

// TestRunNaoRemartelaQuandoDownloadFalha exercita a fiação real (Run), não
// processFile isolada: prova que uma falha de download persistente pausa o
// loop em pollRetryBackoff em vez de voltar direto ao topo sem dormir. Sem o
// guard, 300ms de Run() sem nenhum sleep geram milhares de List() — a
// asserção <=8 tem margem enorme sobre os ~6 ciclos esperados com backoff de
// 50ms.
func TestRunNaoRemartelaQuandoDownloadFalha(t *testing.T) {
	backoffOriginal := pollRetryBackoff
	pollRetryBackoff = 50 * time.Millisecond
	t.Cleanup(func() { pollRetryBackoff = backoffOriginal })

	src := &fakeSourceAlwaysFailsOpen{}
	p := &Pipeline{
		SFTPFolder:  "pb",
		TempDir:     t.TempDir(),
		WaitingTime: 50 * time.Millisecond,
		Src:         src,
		Sender:      discord.NewSender(nil, 1),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	p.Run(ctx)

	if calls := src.calls(); calls > 8 {
		t.Errorf("List() chamado %d vezes em 300ms — sem o guard de backoff, download que sempre falha vira busy-loop sem sleep", calls)
	}
}

func (t *trackedReadCloser) Read(p []byte) (int, error) { return t.r.Read(p) }
func (t *trackedReadCloser) Close() error                { *t.aberto = false; return nil }

// closeCorruptsLocal simula, no Close() do handle remoto (que em processFile
// roda logo ANTES do os.ReadFile — pipeline.go:189-191), um erro de disco/
// permissão no arquivo local recém-baixado: apaga o .png e recria o caminho
// como diretório, forçando o ReadFile seguinte a falhar com EISDIR de forma
// determinística, sem depender de root/permissão real. processFile fecha o
// remote duas vezes (explícito na linha 189 e via defer na 174, documentado
// como idempotente) — Close() só corrompe na primeira chamada, senão a
// segunda recria o diretório depois do os.Remove do próprio fix e mascara o
// comportamento que este teste prova.
type closeCorruptsLocal struct {
	r         io.Reader
	localPath string
	fechado   bool
}

func (c *closeCorruptsLocal) Read(p []byte) (int, error) { return c.r.Read(p) }
func (c *closeCorruptsLocal) Close() error {
	if c.fechado {
		return nil
	}
	c.fechado = true
	os.Remove(c.localPath)
	return os.Mkdir(c.localPath, 0o755)
}

// fakeSourceReadFail devolve um closeCorruptsLocal no Open, pra exercitar o
// ramo de erro do os.ReadFile em processFile. deleteCalls prova que o remoto
// não é tocado nesse ramo — só o local.
type fakeSourceReadFail struct {
	tempDir     string
	deleteCalls int
}

func (f *fakeSourceReadFail) EnsureConnected() error { return nil }
func (f *fakeSourceReadFail) List(string) ([]source.FileInfo, error) {
	return nil, errors.New("nao deve ser chamado")
}
func (f *fakeSourceReadFail) Open(dir, name string) (io.ReadCloser, error) {
	return &closeCorruptsLocal{r: strings.NewReader("png-falso"), localPath: filepath.Join(f.tempDir, name)}, nil
}
func (f *fakeSourceReadFail) ModTime(string, string) (time.Time, error) {
	return time.Time{}, errors.New("nao deve ser chamado")
}
func (f *fakeSourceReadFail) Delete(dir, name string) error {
	f.deleteCalls++
	return nil
}
func (f *fakeSourceReadFail) Close() error { return nil }

// TestProcessFileApagaLocalQuandoReReadFalha prova o defeito da proposta do
// enxame de 28/08: antes desta correção, uma falha no os.ReadFile pós-download
// (pipeline.go:191-195) retornava sem apagar o .png, deixando-o órfão no
// TempDir até a retenção do janitor. Exercita processFile de ponta a ponta —
// o mesmo método que Run() chama a cada arquivo listado (pipeline.go:102) —
// e prova, junto, que o remoto é preservado e o inFlight é liberado (nada foi
// enfileirado). A prova por mutação é remover o novo os.Remove(localPath) do
// ramo de erro: só a asserção 1 (arquivo local sumiu) cai; 2 e 3 continuam
// verdes de propósito, porque travam o comportamento que o fix NÃO pode mudar.
func TestProcessFileApagaLocalQuandoReReadFalha(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSourceReadFail{tempDir: dir}
	p := &Pipeline{
		ServerLabel: "servidor-de-teste",
		SFTPFolder:  "pb",
		TempDir:     dir,
		Src:         src,
		Sender:      discord.NewSender(nil, 1),
	}
	localPath := filepath.Join(dir, "pb000006.png")

	p.inFlight.Store("pb000006.png", true)
	p.processFile(source.FileInfo{Name: "pb000006.png", Size: 2000, ModTime: time.Now()})

	if _, err := os.Lstat(localPath); !os.IsNotExist(err) {
		t.Errorf("arquivo local deveria ter sido apagado apos falha de ReadFile, err=%v", err)
	}
	if src.deleteCalls != 0 {
		t.Errorf("remoto nao deveria ter sido apagado nesse ramo, Delete chamado %d vez(es)", src.deleteCalls)
	}
	if _, aindaEmVoo := p.inFlight.Load("pb000006.png"); aindaEmVoo {
		t.Errorf("inFlight deveria ter sido liberado (nada foi enfileirado)")
	}
}

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

// fakeSourceContent serve um conteúdo fixo de arquivo via Open, pra exercitar
// o caminho de parsing de processFile com um header específico.
type fakeSourceContent struct {
	data []byte
}

func (f *fakeSourceContent) EnsureConnected() error { return nil }
func (f *fakeSourceContent) List(string) ([]source.FileInfo, error) {
	return nil, errors.New("nao deve ser chamado")
}
func (f *fakeSourceContent) Open(string, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(f.data))), nil
}
func (f *fakeSourceContent) ModTime(string, string) (time.Time, error) {
	return time.Time{}, errors.New("nao deve ser chamado")
}
func (f *fakeSourceContent) Delete(string, string) error { return errors.New("nao deve ser chamado") }
func (f *fakeSourceContent) Close() error                { return nil }

// TestProcessFileBannerDeServidorNaoViraGUID exercita a fiação real
// (processFile, pipeline.go:170) com um header cuja linha 4 é um banner de
// servidor (medido em produção: 1554 screenshots com GUID decimal de 6-8
// dígitos) — não a função parser.Extract isolada. Prova que o guard novo do
// parser (Empty=true para a linha de banner) chega até o mesmo tratamento já
// testado para GUID ausente (pipeline.go:223-227): o WARN "sem GUID" dispara,
// e ele só dispara nesse ramo — as duas linhas seguintes (GUID="unknown",
// PlayerName="(sem GUID)") são incondicionais dentro do mesmo bloco, então o
// WARN é um proxy fiel de que a atribuição de jogador foi descartada. A
// mutação que derruba este teste é remover o guard do parser (volta a aceitar
// "944369 ..." como GUID): o WARN não dispara e o teste falha.
func TestProcessFileBannerDeServidorNaoViraGUID(t *testing.T) {
	dir := t.TempDir()
	header := strings.Join([]string{
		"BF4", "svss", "pedro.fragify.net:2025", "2026-06-09 18:50:49",
		"944369 131.196.199.123:25220 !          !DuckDuck Op.Locker.60hp",
	}, "\n") + "\n"
	data := append([]byte(header), []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}...)
	src := &fakeSourceContent{data: data}

	buf := &bytes.Buffer{}
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	defer slog.SetDefault(origLogger)

	p := &Pipeline{
		ServerLabel: "servidor-de-teste",
		SFTPFolder:  "pb",
		TempDir:     dir,
		Src:         src,
		Sender:      discord.NewSender(nil, 1),
	}

	f := source.FileInfo{Name: "pb999099.png", Size: int64(len(data)), ModTime: time.Now()}
	p.inFlight.Store(f.Name, struct{}{})
	if ok := p.processFile(f); !ok {
		t.Fatalf("processFile deveria ter enfileirado o arquivo com sucesso")
	}

	logado := buf.String()
	if !strings.Contains(logado, "cabeçalho do screenshot veio sem GUID") {
		t.Fatalf("esperava WARN de header sem atribuicao de jogador para a linha de banner, log: %q", logado)
	}
	// Prova a fiação de Info.RawLine até o WARN real (não só o campo no struct):
	// mutação que remove "linha4", info.RawLine de pipeline.go:224 derruba esta
	// asserção sem afetar o teste de unidade em pbheader_test.go.
	if !strings.Contains(logado, "linha4=") || !strings.Contains(logado, "131.196.199.123:25220") {
		t.Fatalf("esperava a linha 4 crua (linha4=...) no WARN, log: %q", logado)
	}
	if _, aindaEmVoo := p.inFlight.Load(f.Name); !aindaEmVoo {
		t.Errorf("inFlight nao deveria ter sido liberado: processFile so libera em falha, e este enfileirou com sucesso")
	}
}
