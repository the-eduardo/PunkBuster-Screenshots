package queue

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"pbss/internal/discord"
	"pbss/internal/source"
)

// cabecalhoNormal é um header de PunkBuster bem-formado: GUID direto na linha
// 4 (0-based), sem deslocamento e sem linha vazia — o caminho feliz de
// parser.Extract, não o de recuperação.
func cabecalhoNormal() []byte {
	header := strings.Join([]string{
		"BF4", "svss", "pedro.fragify.net:2025", "2026-06-09 18:50:49",
		"*5416a6f4ea15c7a4782f4bf64dab0182* JoseToalha",
	}, "\n") + "\n"
	return append([]byte(header), []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}...)
}

// comLoggerEm redireciona slog.Default() pro nível e buffer dados, restaurando
// o logger original ao fim do teste — mesmo harness de pipeline_test.go:715.
func comLoggerEm(t *testing.T, nivel slog.Level) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: nivel})))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return buf
}

// TestProcessFileLogaDebugScreenshotEnfileiradoComNivelDebug prova a fiação
// DEBUG_MODE -> nível do handler -> processFile: com o handler em
// slog.LevelDebug, o enfileiramento de um screenshot tem que aparecer no log,
// carregando o GUID real extraído — não só a string fixa.
func TestProcessFileLogaDebugScreenshotEnfileiradoComNivelDebug(t *testing.T) {
	buf := comLoggerEm(t, slog.LevelDebug)
	data := cabecalhoNormal()
	p := &Pipeline{
		ServerLabel: "servidor-de-teste",
		SFTPFolder:  "pb",
		TempDir:     t.TempDir(),
		Src:         &fakeSourceContent{data: data},
		Sender:      discord.NewSender(nil, 1),
	}
	f := source.FileInfo{Name: "pb999200.png", Size: int64(len(data)), ModTime: time.Now()}
	if ok := p.processFile(f); !ok {
		t.Fatalf("processFile deveria ter enfileirado o arquivo")
	}
	logado := buf.String()
	if !strings.Contains(logado, "screenshot enfileirado") {
		t.Fatalf("esperava debug de screenshot enfileirado com nivel Debug, log: %q", logado)
	}
	if !strings.Contains(logado, "*5416a6f4ea15c7a4782f4bf64dab0182*") {
		t.Fatalf("esperava o GUID real no debug, log: %q", logado)
	}
}

// TestProcessFileSilenciaScreenshotEnfileiradoComNivelInfo prova o gate: o
// mesmo fluxo, com o handler no nível padrão de produção (Info), não deve
// produzir a linha de debug — sem isso o teste de cima provaria só que a
// string existe em algum lugar, não que o nível realmente filtra.
func TestProcessFileSilenciaScreenshotEnfileiradoComNivelInfo(t *testing.T) {
	buf := comLoggerEm(t, slog.LevelInfo)
	data := cabecalhoNormal()
	p := &Pipeline{
		ServerLabel: "servidor-de-teste",
		SFTPFolder:  "pb",
		TempDir:     t.TempDir(),
		Src:         &fakeSourceContent{data: data},
		Sender:      discord.NewSender(nil, 1),
	}
	f := source.FileInfo{Name: "pb999201.png", Size: int64(len(data)), ModTime: time.Now()}
	if ok := p.processFile(f); !ok {
		t.Fatalf("processFile deveria ter enfileirado o arquivo")
	}
	if logado := buf.String(); strings.Contains(logado, "screenshot enfileirado") {
		t.Fatalf("nivel Info nao deveria logar o debug de screenshot enfileirado, log: %q", logado)
	}
}

// fakeSourcePollOnly serve List() vazio indefinidamente — o suficiente pra
// exercitar um ciclo de Run() sem nenhum arquivo, onde o único efeito
// observável é o debug de poll concluído antes do switch pending/failed.
type fakeSourcePollOnly struct{}

func (f *fakeSourcePollOnly) EnsureConnected() error { return nil }
func (f *fakeSourcePollOnly) List(string) ([]source.FileInfo, error) {
	return nil, nil
}
func (f *fakeSourcePollOnly) Open(string, string) (io.ReadCloser, error) {
	return nil, errors.New("nao deve ser chamado")
}
func (f *fakeSourcePollOnly) ModTime(string, string) (time.Time, error) {
	return time.Time{}, errors.New("nao deve ser chamado")
}
func (f *fakeSourcePollOnly) Delete(string, string) error {
	return errors.New("nao deve ser chamado")
}
func (f *fakeSourcePollOnly) Close() error { return nil }

// TestRunLogaDebugPollConcluidoComNivelDebug prova a fiação no outro call
// site: o Run() que o main() de fato chama a cada ciclo precisa emitir o
// debug de poll concluído quando o nível é Debug.
func TestRunLogaDebugPollConcluidoComNivelDebug(t *testing.T) {
	buf := comLoggerEm(t, slog.LevelDebug)
	p := &Pipeline{
		SFTPFolder:  "pb",
		TempDir:     t.TempDir(),
		WaitingTime: time.Hour,
		Src:         &fakeSourcePollOnly{},
		Sender:      discord.NewSender(nil, 1),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	p.Run(ctx)

	if logado := buf.String(); !strings.Contains(logado, "poll concluido") {
		t.Fatalf("esperava debug de poll concluido com nivel Debug, log: %q", logado)
	}
}

// TestRunSilenciaPollConcluidoComNivelInfo é o gate do caso acima: no nível
// padrão de produção, o mesmo ciclo não deve produzir a linha de debug.
func TestRunSilenciaPollConcluidoComNivelInfo(t *testing.T) {
	buf := comLoggerEm(t, slog.LevelInfo)
	p := &Pipeline{
		SFTPFolder:  "pb",
		TempDir:     t.TempDir(),
		WaitingTime: time.Hour,
		Src:         &fakeSourcePollOnly{},
		Sender:      discord.NewSender(nil, 1),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	p.Run(ctx)

	if logado := buf.String(); strings.Contains(logado, "poll concluido") {
		t.Fatalf("nivel Info nao deveria logar o debug de poll concluido, log: %q", logado)
	}
}
