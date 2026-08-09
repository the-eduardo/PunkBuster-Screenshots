package queue

import (
	"errors"
	"io"
	"os"
	"path/filepath"
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
