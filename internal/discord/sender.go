package discord

import (
	"fmt"
	"log/slog"
	"os"
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
}

func NewSender(session *discordgo.Session, queueSize int) *Sender {
	return &Sender{session: session, jobs: make(chan SendJob, queueSize)}
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
			job.Done(SendResult{GuildID: msg.GuildID, ChannelID: msg.ChannelID, MessageID: msg.ID})
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
