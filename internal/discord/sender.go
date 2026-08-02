package discord

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// SendJob representa um screenshot pronto para ser enviado ao Discord.
type SendJob struct {
	ChannelID  string
	LocalPath  string // caminho do arquivo já baixado no disco local
	FileName   string
	GUID       string
	PlayerName string
	CapturedAt time.Time

	// Done é chamado exatamente uma vez com o resultado do envio (sucesso ou falha
	// definitiva após as tentativas de retry). É aqui que o pipeline decide gravar
	// no banco e apagar os arquivos local/remoto.
	Done func(res SendResult)
}

// SendResult é o resultado de um envio, já com os IDs necessários pra gravar no índice.
type SendResult struct {
	GuildID   string
	ChannelID string
	MessageID string
	Err       error
}

// Sender processa jobs de envio sequencialmente numa goroutine própria. Processar
// em série evita martelar o rate limit do Discord — o discordgo já respeita
// automaticamente os limites por rota, então uma fila serial é suficiente aqui
// dado o volume esperado (não é preciso um limitador próprio).
type Sender struct {
	session *discordgo.Session
	jobs    chan SendJob

	// guildCache memoriza o guild de cada canal. O lookup e' feito uma vez por
	// canal e o resultado nao muda em runtime, entao nao vale pagar REST por
	// screenshot (o volume passa de 85 mil).
	guildCache sync.Map
}

func NewSender(session *discordgo.Session, queueSize int) *Sender {
	return &Sender{session: session, jobs: make(chan SendJob, queueSize)}
}

// guildFor descobre a que servidor o canal pertence.
//
// Existe porque a resposta REST de criacao de mensagem NAO traz `guild_id` —
// esse campo so vem em evento de gateway. Como o SendResult lia msg.GuildID,
// todos os 85.772 registros do indice ficaram com o guild vazio e o link
// "ver no Discord" do /search nunca apareceu, sem nenhum erro no log.
//
// Primeiro tenta o cache de State (populado pelo IntentsGuilds, sem custo de
// rede); se o canal ainda nao estiver la, cai pro REST uma unica vez.
func (s *Sender) guildFor(channelID string) string {
	if channelID == "" {
		return ""
	}
	if v, ok := s.guildCache.Load(channelID); ok {
		return v.(string)
	}

	var guildID string
	if ch, err := s.session.State.Channel(channelID); err == nil && ch.GuildID != "" {
		guildID = ch.GuildID
	} else if ch, err := s.session.Channel(channelID); err == nil {
		guildID = ch.GuildID
	} else {
		slog.Warn("nao consegui descobrir o guild do canal; o link do /search ficara indisponivel para estes envios",
			"canal", channelID, "erro", err)
		return ""
	}

	if guildID != "" {
		s.guildCache.Store(channelID, guildID)
	}
	return guildID
}

// Enqueue adiciona um job à fila. Bloqueia se a fila estiver cheia (aplica
// backpressure no polling em vez de estourar memória em picos de 2000+ arquivos).
func (s *Sender) Enqueue(job SendJob) {
	s.jobs <- job
}

// Run consome a fila até o canal ser fechado. Deve rodar em goroutine própria.
func (s *Sender) Run() {
	for job := range s.jobs {
		s.process(job)
	}
}

func (s *Sender) Close() {
	close(s.jobs)
}

const maxAttempts = 4

func (s *Sender) process(job SendJob) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		msg, err := s.send(job)
		if err == nil {
			// msg.GuildID vem vazio pela REST; guildFor cobre isso. O `if` mantem
			// o valor da API caso o Discord passe a preenche-lo um dia.
			guildID := msg.GuildID
			if guildID == "" {
				guildID = s.guildFor(msg.ChannelID)
			}
			job.Done(SendResult{GuildID: guildID, ChannelID: msg.ChannelID, MessageID: msg.ID})
			return
		}
		lastErr = err
		slog.Warn("falha ao enviar screenshot ao discord, tentando de novo",
			"arquivo", job.FileName, "tentativa", attempt, "erro", err)
		time.Sleep(time.Duration(attempt*attempt) * time.Second)
	}
	job.Done(SendResult{Err: fmt.Errorf("falhou após %d tentativas: %w", maxAttempts, lastErr)})
}

func (s *Sender) send(job SendJob) (*discordgo.Message, error) {
	f, err := os.Open(job.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("não foi possível abrir %s: %w", job.LocalPath, err)
	}
	defer f.Close()

	capturedStr := "desconhecida"
	if !job.CapturedAt.IsZero() {
		capturedStr = job.CapturedAt.Format("2006-01-02 15:04:05")
	}

	content := fmt.Sprintf("File: %s | Created at: %s\nPBGUID: %s %s",
		job.FileName, capturedStr, job.GUID, job.PlayerName)

	return s.session.ChannelMessageSendComplex(job.ChannelID, &discordgo.MessageSend{
		Content: content,
		Files: []*discordgo.File{
			{Name: job.FileName, Reader: f},
		},
	})
}
