// Package queue orquestra o ciclo de vida de cada screenshot: listar no
// SFTP/FTP, baixar para uma pasta temporária local, extrair o GUID, enfileirar
// o envio ao Discord e só então confirmar a limpeza (local + remoto).
//
// O disco local é só uma fila de trânsito (o Discord é o armazenamento
// permanente de fato) — por isso um "janitor" força a limpeza de qualquer
// arquivo temporário mais velho que a retenção configurada, mesmo que o envio
// nunca tenha sido confirmado.
package queue

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"pbss/internal/discord"
	"pbss/internal/parser"
	"pbss/internal/source"
	"pbss/internal/storage"
)

type Pipeline struct {
	ServerLabel    string
	SFTPFolder     string
	TempDir        string
	WaitingTime    time.Duration
	RetentionHours int

	Src    source.Source
	Store  *storage.Store
	Sender *discord.Sender

	ChannelID string

	// inFlight rastreia arquivos já baixados e enfileirados, mas ainda sem
	// confirmação de envio. Evita que o poller (que volta a listar o diretório
	// remoto imediatamente quando há backlog) baixe e enfileire o mesmo arquivo
	// de novo antes do envio/exclusão remota anterior serem confirmados — o que
	// causava "no such file or directory" quando o job duplicado tentava abrir
	// um arquivo local já apagado pelo job original.
	inFlight sync.Map

	// lastPoll marca o último List() bem-sucedido do diretório remoto. É o sinal
	// de vida do poller: atualiza mesmo quando não há arquivo nenhum, então a
	// madrugada vazia do servidor não gera falso alarme.
	lastPoll atomic.Int64
}

func (p *Pipeline) Run(ctx context.Context) error {
	if err := os.MkdirAll(p.TempDir, 0o755); err != nil {
		return fmt.Errorf("falha ao criar diretório temporário %s: %w", p.TempDir, err)
	}

	go p.runJanitor(ctx)

	// Store otimista logo no boot: é uma janela de graça, não a confirmação de
	// um List() bem-sucedido. Se o SFTP/FTP estiver mal configurado desde o
	// início (credencial errada, host inalcançável), PollAlive() ainda reporta
	// "vivo" por até pollStaleAfter após cada restart — aceitável porque (a) é
	// estritamente melhor que o comportamento anterior a esta mudança, que não
	// tinha checagem de poller nenhuma, e (b) segue o mesmo raciocínio de
	// pollStaleAfter: dar tempo do servidor remoto acordar sem gerar ruído de
	// reboot. Achado do comitê rápido de 19/08/2026 (item 3).
	p.lastPoll.Store(time.Now().Unix())

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := p.Src.EnsureConnected(); err != nil {
			slog.Error("falha ao conectar na origem dos screenshots, tentando de novo em 30s", "erro", err)
			sleepOrDone(ctx, 30*time.Second)
			continue
		}

		files, err := p.Src.List(p.SFTPFolder)
		if err != nil {
			slog.Error("falha ao listar diretório remoto, tentando de novo em 30s", "erro", err)
			sleepOrDone(ctx, 30*time.Second)
			continue
		}
		p.lastPoll.Store(time.Now().Unix())

		pending, failed := 0, 0
		for _, f := range files {
			if !f.IsScreenshot() {
				continue
			}
			if _, alreadyQueued := p.inFlight.LoadOrStore(f.Name, struct{}{}); alreadyQueued {
				continue // já baixado/enfileirado numa passada anterior, aguardando confirmação
			}
			if p.processFile(f) {
				pending++
			} else {
				failed++
			}
		}

		switch {
		case pending > 0:
			// Backlog real enfileirado: volta imediato pro topo do loop pra checar
			// se chegaram mais arquivos durante o processamento, sem esperar
			// WaitingTime — comportamento inalterado por esta correção.
		case failed > 0:
			// processFile falhou pra todo mundo que tentou (Open/Create/Copy/
			// ReadFile — ver os returns dentro dela). Sem essa pausa, o loop volta
			// direto ao topo sem dormir, reencontra o mesmo arquivo (o inFlight já
			// foi liberado pelo defer de processFile) e falha de novo: busy-loop
			// martelando a origem remota sem sleep nenhum. Se a causa for
			// transitória, 30s já resolve; se for permanente, ao menos não trava
			// a CPU nem enche o log em rajada.
			sleepOrDone(ctx, pollRetryBackoff)
		default:
			sleepOrDone(ctx, p.WaitingTime)
		}
	}
}

// pollRetryBackoff é a pausa aplicada quando processFile falha para todo
// arquivo pendente do ciclo (busy-loop guard). Var (não const) seguindo o
// mesmo padrão já aceito em pollStaleGrace/sftpProbeTimeout/janitorInterval,
// e espelha os 30s literais que EnsureConnected/List já usam em seus ramos
// de erro (não mexidos aqui — escopo mínimo).
var pollRetryBackoff = 30 * time.Second

// pollStaleGrace e' a folga POR CIMA do ciclo de poll (WaitingTime) que o
// PollAlive tolera antes de considerar o poller morto. 15min por decisao do
// Eduardo (17/08/2026): a maquina do servidor de origem as vezes demora a
// reiniciar, e uma folga mais curta viraria ruido de reboot. Var (nao const)
// pra teste poder encurtar sem esperar 15min de verdade.
//
// Corrigido em 20/08/2026: a calibracao original tratava essa constante como
// o PRAZO TOTAL (fixo em 15min, independente de WaitingTime), sobre a premissa
// errada de que WAITING_TIME e' em segundos. E' em minutos
// (config.go:82-86: Duration(waitingMinutes)*time.Minute) — com o valor de
// producao (20), o poller fica ate 20min em silencio entre List() sem arquivo
// novo, e um prazo fixo de 15min julgava esse silencio normal como poller
// morto em todo ciclo ocioso (49 WARN/dia medidos). O prazo agora e' o ciclo
// real mais a folga.
var pollStaleGrace = 15 * time.Minute

// pollStaleAfter e' o prazo de silencio do poller que o PollAlive tolera antes
// de considerar o poller morto: o proprio ciclo de espera (WaitingTime) mais a
// folga de reboot (pollStaleGrace).
func (p *Pipeline) pollStaleAfter() time.Duration {
	return p.WaitingTime + pollStaleGrace
}

// PollAlive diz se o poller listou o diretório remoto recentemente.
func (p *Pipeline) PollAlive() bool {
	last := p.lastPoll.Load()
	if last == 0 {
		return false
	}
	return time.Since(time.Unix(last, 0)) < p.pollStaleAfter()
}

// processFile devolve true se o arquivo foi enfileirado com sucesso pro
// Sender, false em qualquer uma das 4 falhas de I/O (Open/Create/Copy/
// ReadFile) — o retorno alimenta o guard de busy-loop em Run().
func (p *Pipeline) processFile(f source.FileInfo) bool {
	localPath := filepath.Join(p.TempDir, f.Name)

	// Libera a entrada de inFlight se sairmos antes de enfileirar com sucesso
	// (qualquer "return" abaixo). Se enfileirar, quem libera é onSendResult,
	// só depois que o envio for confirmado (ou definitivamente falhar).
	queued := false
	defer func() {
		if !queued {
			p.inFlight.Delete(f.Name)
		}
	}()

	// ModTime vem ANTES do Open: no FTP, Open/Retr abre uma conexão de dados
	// cujo Close consome o "226" pendente na conexão de CONTROLE (doc do
	// jlaffaye/ftp), e qualquer comando emitido nessa mesma conexão antes desse
	// Close (como MDTM) lê a resposta errada e desalinha todas as respostas
	// seguintes. MDTM antes de RETR é uso legal de FTP e o valor não muda, já
	// que o arquivo não é escrito entre as duas chamadas.
	capturedAt := f.ModTime
	if mt, err := p.Src.ModTime(p.SFTPFolder, f.Name); err == nil {
		capturedAt = mt
	}

	remote, err := p.Src.Open(p.SFTPFolder, f.Name)
	if err != nil {
		slog.Error("não foi possível abrir arquivo remoto", "arquivo", f.Name, "erro", err)
		return false
	}
	defer remote.Close()

	local, err := os.Create(localPath)
	if err != nil {
		slog.Error("não foi possível criar arquivo local", "arquivo", localPath, "erro", err)
		return false
	}

	if _, err := io.Copy(local, remote); err != nil {
		local.Close()
		os.Remove(localPath)
		slog.Error("falha ao baixar arquivo", "arquivo", f.Name, "erro", err)
		return false
	}
	local.Close()
	remote.Close() // fecha o handle de dados antes de qualquer novo comando na conexão de controle (o defer acima cobre os returns adiantados; Close é idempotente)

	data, err := os.ReadFile(localPath)
	if err != nil {
		os.Remove(localPath)
		slog.Error("falha ao reler arquivo local pra extrair GUID", "arquivo", localPath, "erro", err)
		return false
	}
	info := parser.Extract(data)
	if info.Empty {
		slog.Warn("cabeçalho do screenshot veio sem GUID (bug conhecido do PunkBuster), enviando sem atribuição de jogador", "arquivo", f.Name, "linha4", info.RawLine)
		info.GUID = "unknown"
		info.PlayerName = "(sem GUID)"
	}

	dir, name := p.SFTPFolder, f.Name
	p.Sender.Enqueue(discord.SendJob{
		ChannelID:  p.ChannelID,
		LocalPath:  localPath,
		FileName:   f.Name,
		GUID:       info.GUID,
		PlayerName: info.PlayerName,
		CapturedAt: capturedAt,
		Done: func(res discord.SendResult) {
			p.onSendResult(dir, name, localPath, info, capturedAt, res)
		},
	})
	queued = true
	return true
}

func (p *Pipeline) onSendResult(dir, name, localPath string, info parser.Info, capturedAt time.Time, res discord.SendResult) {
	// Libera o arquivo pro poller considerar de novo: se falhou definitivamente
	// e o remoto ainda existe (não apagado), o próximo ciclo de poll o pega
	// normalmente, agora sem risco de duplicar (o download anterior já concluiu).
	defer p.inFlight.Delete(name)

	if res.Err != nil {
		slog.Error("envio ao discord falhou definitivamente, arquivo mantido pra nova tentativa",
			"arquivo", name, "erro", res.Err)
		return
	}

	err := p.Store.RecordScreenshot(storage.ScreenshotRecord{
		GUID:             info.GUID,
		PlayerName:       info.PlayerName,
		FileName:         name,
		CapturedAt:       capturedAt,
		ReceivedAt:       time.Now().UTC(),
		Server:           p.ServerLabel,
		DiscordGuildID:   res.GuildID,
		DiscordChannelID: res.ChannelID,
		DiscordMessageID: res.MessageID,
	})
	if err != nil {
		slog.Error("mensagem enviada mas falhou ao gravar no índice sqlite", "arquivo", name, "erro", err)
		// Mesmo assim segue com a limpeza: o Discord já tem a imagem, o índice é
		// só um atalho de busca — perder uma linha de índice não justifica reter o arquivo.
	}

	if err := os.Remove(localPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("falha ao apagar arquivo local", "arquivo", localPath, "erro", err)
	}
	if err := p.Src.Delete(dir, name); err != nil {
		slog.Warn("falha ao apagar arquivo remoto (pode ser reenviado no próximo ciclo)", "arquivo", name, "erro", err)
	}
}

// janitorInterval e' o intervalo entre varreduras do janitor. Var (nao const)
// pra teste poder encurtar sem esperar 15min de verdade, seguindo o mesmo
// padrao ja aceito em pollStaleGrace e sftpProbeTimeout.
var janitorInterval = 15 * time.Minute

// retentionMaxAge e' a idade a partir da qual o janitor apaga um temporario.
// RetentionHours <= 0 significa "nao configurado" — Pipeline e' struct publica
// sem validacao propria (o helper newTestPipeline em pipeline_test.go ja
// constroi um &Pipeline{} com o campo zerado) — e maxAge=0 faria o janitor
// apagar TODO arquivo do TempDir a cada tick, inclusive os que estao em voo
// aguardando confirmacao de envio. Espelha o default de config.go:105.
func (p *Pipeline) retentionMaxAge() time.Duration {
	if p.RetentionHours <= 0 {
		return 24 * time.Hour
	}
	return time.Duration(p.RetentionHours) * time.Hour
}

// runJanitor força a limpeza de qualquer arquivo temporário mais velho que a
// retenção configurada, mesmo sem confirmação de envio. É a rede de segurança
// contra acúmulo de disco em cenários de falha prolongada do Discord.
func (p *Pipeline) runJanitor(ctx context.Context) {
	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if removed := p.sweepTempDir(); removed > 0 {
				slog.Warn("janitor: arquivos temporários expirados removidos sem confirmação de envio", "quantidade", removed)
			}
		}
	}
}

// sweepTempDir apaga do TempDir todo arquivo mais velho que retentionMaxAge()
// e devolve quantos arquivos foram removidos.
func (p *Pipeline) sweepTempDir() int {
	entries, err := os.ReadDir(p.TempDir)
	if err != nil {
		slog.Error("janitor: falha ao listar diretório temporário", "erro", err)
		return 0
	}
	maxAge := p.retentionMaxAge()
	removed := 0
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) > maxAge {
			if err := os.Remove(filepath.Join(p.TempDir, e.Name())); err == nil {
				removed++
			}
		}
	}
	return removed
}

func sleepOrDone(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
