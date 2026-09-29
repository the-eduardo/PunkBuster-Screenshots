// Package commands implementa os slash commands do mini-dashboard (/pbss),
// que consultam o índice sqlite de screenshots já confirmados no Discord.
package commands

import (
	"fmt"
	"log/slog"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"pbss/internal/storage"
)

var guidPattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

const pageSize = 5

// embedDescBudget é o teto que formatEntries respeita ao montar a descrição
// do /pbss last, com margem sob o limite real do Discord (4096 chars) pra
// caber o aviso de omissão. Medido em produção: a pior janela real de 20
// linhas consecutivas chega a 4014/4096 — sem esse teto, o primeiro /pbss
// last com nomes longos estoura o limite e o Discord rejeita a resposta
// (silenciosamente, ver respondEphemeral). Clampar por bytes (len) é
// deliberadamente conservador: o Discord conta unidades UTF-16 e nomes do PB
// têm char não-ASCII, então byte-count sempre superestima — erra pro lado
// seguro, nunca pro estouro.
const embedDescBudget = 3950

// contentBudget é o teto que respondEphemeral respeita no Content, com
// margem sob o limite real do Discord (2000) pra caber o aviso de corte. A
// option "termo" do /pbss hoje tem MaxLength (termoMaxLength), mas o clamp
// continua como defesa em profundidade: um comando registrado antes dele, ou
// um client que ignore o limite, ainda pode mandar até 6000 chars — e um termo
// longo nunca casa em nenhum LIKE (o maior player_name em produção tem 73
// chars), então cai sempre no ramo "nenhum resultado", ecoando o termo cru no
// Content. Contar bytes é a mesma escolha
// conservadora do embedDescBudget acima: erra pro lado seguro.
const contentBudget = 1900

// clampContent trunca no contentBudget recuando até a fronteira de rune, pra
// nunca partir um caractere UTF-8 no meio (mesmo cuidado do
// rawSnippetMaxBytes em parser/pbheader.go).
func clampContent(s string) string {
	if len(s) <= contentBudget {
		return s
	}
	cut := contentBudget
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "… (termo truncado)"
}

// embedFieldBudget é o teto que formatTopPlayers respeita ao montar o field
// "Top 10 mais flagrados" de /pbss stats, sob o limite de field.value do
// Discord (1024 chars — não os 4096 da description). O aviso de omissão é
// ESCRITO DEPOIS do teto ser atingido (mesmo padrão de embedDescBudget), então
// o budget precisa deixar espaço pro próprio aviso: no pior caso (top10, 2
// dígitos) ele tem 51 bytes; 950 deixa margem de ~23 bytes sob o limite real.
// Sem esse teto, ~4 jogadores com nome longo no top10 já estouram o campo e o
// Discord rejeita a resposta inteira.
const embedFieldBudget = 950

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

// termoMaxLength limita as options "termo" do /pbss (search e last). Sem ele o
// Discord aceita até 6000 chars num termo que nunca casa (maior player_name em
// produção: 73 chars; GUID com asteriscos: 34) e o eco vai inteiro pro
// Content — o clampContent segura o estouro, isto aqui corta na origem.
const termoMaxLength = 100

// pbssCommand monta a definição do /pbss. Separada do Register pra suíte
// inspecionar a struct sem rede.
func pbssCommand() *discordgo.ApplicationCommand {
	return &discordgo.ApplicationCommand{
		Name:        "pbss",
		Description: "Consulta o histórico de screenshots do PunkBuster",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "search",
				Description: "Busca screenshots por nome ou GUID",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionString, Name: "termo", Description: "Nome do jogador ou GUID", Required: true, MaxLength: termoMaxLength},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "last",
				Description: "Mostra os últimos screenshots de um jogador, sem paginação",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionString, Name: "termo", Description: "Nome do jogador ou GUID", Required: true, MaxLength: termoMaxLength},
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
}

// Register cria o comando de aplicação. guildID vazio registra globalmente
// (demora até 1h pra propagar); um guildID específico propaga na hora, útil pra testes.
// ApplicationCommandCreate é upsert SÓ do /pbss, e roda em todo boot — o app
// Discord é compartilhado com o bf4db-bot, então NUNCA trocar por bulk
// overwrite (apagaria os comandos do outro bot).
func (h *Handler) Register(s *discordgo.Session, guildID string) error {
	_, err := s.ApplicationCommandCreate(s.State.User.ID, guildID, pbssCommand())
	return err
}

// respond é o único ponto do pacote que chama s.InteractionRespond
// diretamente: toda rejeição do Discord (limite, 4xx, rede) vira slog.Error
// em vez de sumir em silêncio — o usuário só veria "The application did not
// respond" sem nada no log pra investigar depois.
func respond(s *discordgo.Session, i *discordgo.InteractionCreate, resp *discordgo.InteractionResponse) error {
	err := s.InteractionRespond(i.Interaction, resp)
	if err != nil {
		slog.Error("falha ao responder interacao", "erro", err, "tipo", i.Type)
	}
	return err
}

// HandleInteraction roteia comandos e cliques de botão de paginação. O
// discordgo não tem recover próprio e despacha cada interação numa goroutine
// nova (SyncEvents=false em session.go): sem este defer, um panic em
// qualquer handler (indexação posicional em handleCommand, corrida de
// paginação em advancePage/renderPage, etc.) mata o processo inteiro em vez
// de afetar só a interação que o causou.
func (h *Handler) HandleInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		slog.Error("panic no handler de interacao",
			"panic", r, "tipo", i.Type, "stack", string(debug.Stack()))
		// Resposta best-effort. O recover aninhado garante que a própria
		// recuperação nunca vire a causa da morte do processo: um panic
		// dentro de um defer que já recuperou volta a propagar.
		func() {
			defer func() { _ = recover() }()
			h.respondEphemeral(s, i, "Erro interno ao processar o comando. Tente de novo em instantes.", nil, nil)
		}()
	}()

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

	err = respond(s, i, &discordgo.InteractionResponse{
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
	if err != nil {
		slog.Warn("resposta enviada mas sem ID da mensagem; paginacao desta busca ficara indisponivel", "erro", err)
		return
	}
	h.mu.Lock()
	h.states[msg.ID] = state
	h.mu.Unlock()
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

	embed := buildLastEmbed(termo, results)
	h.respondEphemeral(s, i, "", []*discordgo.MessageEmbed{embed}, nil)
}

// buildLastEmbed é o recorte de runLast que monta o embed — separado só pra
// caber num teste sem precisar de *discordgo.Session.
func buildLastEmbed(termo string, results []storage.ScreenshotRecord) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("Últimos %d screenshots — %s", len(results), termo),
		Description: formatEntries(results),
		Color:       0x5865F2,
	}
}

func (h *Handler) runStats(s *discordgo.Session, i *discordgo.InteractionCreate) {
	stats, err := h.Store.GetStats()
	if err != nil {
		h.respondError(s, i, err)
		return
	}

	embed := buildStatsEmbed(stats)
	h.respondEphemeral(s, i, "", []*discordgo.MessageEmbed{embed}, nil)
}

// buildStatsEmbed é o recorte de runStats que monta o embed — separado só pra
// caber num teste sem precisar de *discordgo.Session, mesmo padrão de buildLastEmbed.
func buildStatsEmbed(stats storage.Stats) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title: "Estatísticas do PunkBuster Screenshots",
		Color: 0x5865F2,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Total de screenshots", Value: fmt.Sprintf("%d", stats.TotalScreenshots), Inline: true},
			{Name: "Jogadores distintos flagrados", Value: fmt.Sprintf("%d", stats.TotalPlayers), Inline: true},
			{Name: "Top 10 mais flagrados", Value: formatTopPlayers(stats.TopPlayers)},
		},
	}
}

// formatTopPlayers monta o bloco "Top 10 mais flagrados" com o mesmo guard de
// teto que formatEntries usa pra description: o Discord aceita no máximo 1024
// chars em field.value, e o bloco não tinha nenhum clamp antes desta função.
func formatTopPlayers(top []storage.TopPlayer) string {
	var b strings.Builder
	if len(top) == 0 {
		return "_sem dados ainda_"
	}
	for idx, tp := range top {
		linha := fmt.Sprintf("%d. **%s** (`%s`) — %d screenshots\n", idx+1, tp.Name, tp.GUID, tp.Count)
		if b.Len()+len(linha) > embedFieldBudget {
			fmt.Fprintf(&b, "_… %d jogador(es) omitido(s) (limite do Discord)_", len(top)-idx)
			break
		}
		b.WriteString(linha)
	}
	return b.String()
}

func (h *Handler) handleComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.MessageComponentData()
	if data.CustomID != "pbss_prev" && data.CustomID != "pbss_next" {
		return
	}

	embed, components, ok := h.advancePage(i.Message.ID, data.CustomID)
	if !ok {
		respond(s, i, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{Content: "Essa busca expirou, rode o comando de novo.", Embeds: nil, Components: nil},
		})
		return
	}

	respond(s, i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{Embeds: []*discordgo.MessageEmbed{embed}, Components: components},
	})
}

// advancePage segura h.mu durante o lookup, a mutacao de page e o render
// inteiros. O discordgo despacha cada interacao numa goroutine propria
// (SyncEvents fica false em session.go), entao dois cliques rapidos no mesmo
// botao rodam concorrentes: sem o lock cobrindo as tres etapas, as duas
// goroutines podem ler o mesmo state.page, passar juntas pelo guard de limite
// e incrementar duas vezes, jogando a pagina pra fora da faixa (renderPage
// entao monta results[start:end] com start > end e panica — sem recover no
// discordgo isso mata o processo inteiro). renderPage e formatacao pura de
// string, sem I/O, entao chama-la dentro do lock nao bloqueia nada.
func (h *Handler) advancePage(msgID, customID string) (*discordgo.MessageEmbed, []discordgo.MessageComponent, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	state, ok := h.states[msgID]
	if !ok {
		return nil, nil, false
	}

	if customID == "pbss_prev" && state.page > 0 {
		state.page--
	}
	if customID == "pbss_next" && (state.page+1)*pageSize < len(state.results) {
		state.page++
	}

	embed, components := renderPage(state)
	return embed, components, true
}

func (h *Handler) lookup(termo string, limit int) ([]storage.ScreenshotRecord, error) {
	termo = strings.TrimSpace(termo)
	// O PunkBuster grava o GUID entre asteriscos no header (parser/pbheader.go),
	// e o bot exibe o GUID assim mesmo (formatEntries abaixo, sender.go) — então
	// o usuário pode colar o termo com ou sem os asteriscos. Aparamos as pontas
	// antes de testar o padrão hex puro, senão a forma exibida nunca casa e cai
	// (por engano) em SearchByName.
	semAsteriscos := strings.Trim(termo, "*")
	if guidPattern.MatchString(semAsteriscos) {
		return h.Store.SearchByGUID(strings.ToLower(semAsteriscos), limit)
	}
	return h.Store.SearchByName(termo, limit)
}

func renderPage(state *searchState) (*discordgo.MessageEmbed, []discordgo.MessageComponent) {
	start := state.page * pageSize
	if start > len(state.results) {
		// Defesa em profundidade: advancePage ja impede page sair da faixa sob
		// concorrencia, mas start clampado garante end >= start mesmo se algum
		// outro caminho futuro chegar aqui com state.page fora do esperado.
		start = len(state.results)
	}
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
	for idx, e := range entries {
		captured := "desconhecida"
		if !e.CapturedAt.IsZero() {
			captured = e.CapturedAt.Format("2006-01-02 15:04:05")
		}
		link := ""
		if e.DiscordGuildID != "" && e.DiscordChannelID != "" && e.DiscordMessageID != "" {
			link = fmt.Sprintf(" — [ver no Discord](https://discord.com/channels/%s/%s/%s)",
				e.DiscordGuildID, e.DiscordChannelID, e.DiscordMessageID)
		}
		linha := fmt.Sprintf("**%s** (`%s`) — %s%s\n", e.PlayerName, e.GUID, captured, link)
		if b.Len()+len(linha) > embedDescBudget {
			fmt.Fprintf(&b, "_… %d resultado(s) omitido(s) (limite do Discord)_", len(entries)-idx)
			break
		}
		b.WriteString(linha)
	}
	if b.Len() == 0 {
		return "_nenhum resultado nesta página_"
	}
	return b.String()
}

func (h *Handler) respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string, embeds []*discordgo.MessageEmbed, components []discordgo.MessageComponent) {
	respond(s, i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content:    clampContent(content),
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
