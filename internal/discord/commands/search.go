// Package commands implementa os slash commands do mini-dashboard (/pbss),
// que consultam o índice sqlite de screenshots já confirmados no Discord.
package commands

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"pbss/internal/storage"
)

var guidPattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

const pageSize = 5

type searchState struct {
	query     string
	isGUID    bool
	results   []storage.ScreenshotRecord
	page      int
	createdAt time.Time
}

// Handler registra e atende os slash commands "/pbss".
type Handler struct {
	Store *storage.Store

	mu     sync.Mutex
	states map[string]*searchState // chave: ID da mensagem da resposta efêmera
}

func NewHandler(store *storage.Store) *Handler {
	return &Handler{Store: store, states: make(map[string]*searchState)}
}

// Register cria o comando de aplicação. guildID vazio registra globalmente
// (demora até 1h pra propagar); um guildID específico propaga na hora, útil pra testes.
func (h *Handler) Register(s *discordgo.Session, guildID string) error {
	cmd := &discordgo.ApplicationCommand{
		Name:        "pbss",
		Description: "Consulta o histórico de screenshots do PunkBuster",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "search",
				Description: "Busca screenshots por nome ou GUID",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionString, Name: "termo", Description: "Nome do jogador ou GUID", Required: true},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "last",
				Description: "Mostra os últimos screenshots de um jogador, sem paginação",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionString, Name: "termo", Description: "Nome do jogador ou GUID", Required: true},
					{Type: discordgo.ApplicationCommandOptionInteger, Name: "quantidade", Description: "Quantos mostrar (padrão 5, máx 20)", Required: false},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "stats",
				Description: "Estatísticas gerais de screenshots capturados",
			},
		},
	}
	_, err := s.ApplicationCommandCreate(s.State.User.ID, guildID, cmd)
	return err
}

// HandleInteraction roteia comandos e cliques de botão de paginação.
func (h *Handler) HandleInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		h.handleCommand(s, i)
	case discordgo.InteractionMessageComponent:
		h.handleComponent(s, i)
	}
}

func (h *Handler) handleCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if data.Name != "pbss" || len(data.Options) == 0 {
		return
	}
	sub := data.Options[0]
	switch sub.Name {
	case "search":
		h.runSearch(s, i, sub.Options[0].StringValue())
	case "last":
		qty := 5
		if len(sub.Options) > 1 {
			qty = int(sub.Options[1].IntValue())
		}
		if qty <= 0 || qty > 20 {
			qty = 5
		}
		h.runLast(s, i, sub.Options[0].StringValue(), qty)
	case "stats":
		h.runStats(s, i)
	}
}

func (h *Handler) runSearch(s *discordgo.Session, i *discordgo.InteractionCreate, termo string) {
	results, err := h.lookup(termo, 200)
	if err != nil {
		h.respondError(s, i, err)
		return
	}
	if len(results) == 0 {
		h.respondEphemeral(s, i, fmt.Sprintf("Nenhum screenshot encontrado para **%s**.", termo), nil, nil)
		return
	}

	state := &searchState{query: termo, results: results, page: 0, createdAt: time.Now()}
	embed, components := renderPage(state)

	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds:     []*discordgo.MessageEmbed{embed},
			Components: components,
			Flags:      discordgo.MessageFlagsEphemeral,
		},
	})
	if err != nil {
		return
	}

	msg, err := s.InteractionResponse(i.Interaction)
	if err == nil {
		h.mu.Lock()
		h.states[msg.ID] = state
		h.mu.Unlock()
	}
}

func (h *Handler) runLast(s *discordgo.Session, i *discordgo.InteractionCreate, termo string, qty int) {
	results, err := h.lookup(termo, qty)
	if err != nil {
		h.respondError(s, i, err)
		return
	}
	if len(results) == 0 {
		h.respondEphemeral(s, i, fmt.Sprintf("Nenhum screenshot encontrado para **%s**.", termo), nil, nil)
		return
	}

	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("Últimos %d screenshots — %s", len(results), termo),
		Description: formatEntries(results),
		Color:       0x5865F2,
	}
	h.respondEphemeral(s, i, "", []*discordgo.MessageEmbed{embed}, nil)
}

func (h *Handler) runStats(s *discordgo.Session, i *discordgo.InteractionCreate) {
	stats, err := h.Store.GetStats()
	if err != nil {
		h.respondError(s, i, err)
		return
	}

	var top strings.Builder
	if len(stats.TopPlayers) == 0 {
		top.WriteString("_sem dados ainda_")
	}
	for idx, tp := range stats.TopPlayers {
		fmt.Fprintf(&top, "%d. **%s** (`%s`) — %d screenshots\n", idx+1, tp.Name, tp.GUID, tp.Count)
	}

	embed := &discordgo.MessageEmbed{
		Title: "Estatísticas do PunkBuster Screenshots",
		Color: 0x5865F2,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Total de screenshots", Value: fmt.Sprintf("%d", stats.TotalScreenshots), Inline: true},
			{Name: "Jogadores distintos flagrados", Value: fmt.Sprintf("%d", stats.TotalPlayers), Inline: true},
			{Name: "Top 10 mais flagrados", Value: top.String()},
		},
	}
	h.respondEphemeral(s, i, "", []*discordgo.MessageEmbed{embed}, nil)
}

func (h *Handler) handleComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.MessageComponentData()
	if data.CustomID != "pbss_prev" && data.CustomID != "pbss_next" {
		return
	}

	h.mu.Lock()
	state, ok := h.states[i.Message.ID]
	h.mu.Unlock()
	if !ok {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{Content: "Essa busca expirou, rode o comando de novo.", Embeds: nil, Components: nil},
		})
		return
	}

	if data.CustomID == "pbss_prev" && state.page > 0 {
		state.page--
	}
	if data.CustomID == "pbss_next" && (state.page+1)*pageSize < len(state.results) {
		state.page++
	}

	embed, components := renderPage(state)
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{Embeds: []*discordgo.MessageEmbed{embed}, Components: components},
	})
}

func (h *Handler) lookup(termo string, limit int) ([]storage.ScreenshotRecord, error) {
	termo = strings.TrimSpace(termo)
	if guidPattern.MatchString(termo) {
		return h.Store.SearchByGUID(strings.ToLower(termo), limit)
	}
	return h.Store.SearchByName(termo, limit)
}

func renderPage(state *searchState) (*discordgo.MessageEmbed, []discordgo.MessageComponent) {
	start := state.page * pageSize
	end := start + pageSize
	if end > len(state.results) {
		end = len(state.results)
	}
	pageResults := state.results[start:end]
	totalPages := (len(state.results) + pageSize - 1) / pageSize

	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("Resultados para: %s", state.query),
		Description: formatEntries(pageResults),
		Color:       0x5865F2,
		Footer:      &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Página %d/%d — %d resultado(s)", state.page+1, totalPages, len(state.results))},
	}

	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "◀ Anterior", Style: discordgo.SecondaryButton, CustomID: "pbss_prev", Disabled: state.page == 0},
			discordgo.Button{Label: "Próxima ▶", Style: discordgo.SecondaryButton, CustomID: "pbss_next", Disabled: (state.page+1)*pageSize >= len(state.results)},
		}},
	}
	return embed, components
}

func formatEntries(entries []storage.ScreenshotRecord) string {
	var b strings.Builder
	for _, e := range entries {
		captured := "desconhecida"
		if !e.CapturedAt.IsZero() {
			captured = e.CapturedAt.Format("2006-01-02 15:04:05")
		}
		link := ""
		if e.DiscordGuildID != "" && e.DiscordChannelID != "" && e.DiscordMessageID != "" {
			link = fmt.Sprintf(" — [ver no Discord](https://discord.com/channels/%s/%s/%s)",
				e.DiscordGuildID, e.DiscordChannelID, e.DiscordMessageID)
		}
		fmt.Fprintf(&b, "**%s** (`%s`) — %s%s\n", e.PlayerName, e.GUID, captured, link)
	}
	if b.Len() == 0 {
		return "_nenhum resultado nesta página_"
	}
	return b.String()
}

func (h *Handler) respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string, embeds []*discordgo.MessageEmbed, components []discordgo.MessageComponent) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content:    content,
			Embeds:     embeds,
			Components: components,
			Flags:      discordgo.MessageFlagsEphemeral,
		},
	})
}

func (h *Handler) respondError(s *discordgo.Session, i *discordgo.InteractionCreate, err error) {
	h.respondEphemeral(s, i, fmt.Sprintf("Erro ao consultar o índice: %v", err), nil, nil)
}

// PurgeExpiredStates libera memória de buscas com mais de maxAge; deve rodar periodicamente.
func (h *Handler) PurgeExpiredStates(maxAge time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, state := range h.states {
		if time.Since(state.createdAt) > maxAge {
			delete(h.states, id)
		}
	}
}
