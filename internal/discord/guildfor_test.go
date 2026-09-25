package discord

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

// TestGuildForCacheiaUmLookupPorCanal prova o invariante documentado em
// guildFor: o lookup e feito uma vez por canal e a resposta fica presa no
// guildCache. Sobrescrevemos o canal no State com um guild diferente depois
// da primeira chamada — se guildFor consultasse o State de novo, o segundo
// valor viria de la; como o cache existe, ele continua devolvendo o primeiro.
func TestGuildForCacheiaUmLookupPorCanal(t *testing.T) {
	sess := &discordgo.Session{State: discordgo.NewState()}

	// ChannelAdd de um canal novo (fora de PrivateChannels) exige que o guild
	// ja exista no State — senao devolve ErrStateNotFound e nem insere no
	// channelMap (discordgo State.ChannelAdd, guildMap lookup antes do insert).
	if err := sess.State.GuildAdd(&discordgo.Guild{ID: "G1"}); err != nil {
		t.Fatalf("GuildAdd G1: %v", err)
	}
	if err := sess.State.ChannelAdd(&discordgo.Channel{ID: "C1", GuildID: "G1"}); err != nil {
		t.Fatalf("ChannelAdd C1/G1: %v", err)
	}

	s := &Sender{session: sess}

	if got := s.guildFor("C1"); got != "G1" {
		t.Fatalf("1a chamada: esperava G1, veio %q", got)
	}

	// Canal ja existe no channelMap: ChannelAdd so sobrescreve o ponteiro
	// (*c = *channel), sem precisar que G2 esteja no guildMap.
	if err := sess.State.ChannelAdd(&discordgo.Channel{ID: "C1", GuildID: "G2"}); err != nil {
		t.Fatalf("ChannelAdd C1/G2: %v", err)
	}

	if got := s.guildFor("C1"); got != "G1" {
		t.Fatalf("2a chamada: esperava G1 do cache, veio %q — guildFor nao esta cacheando", got)
	}
}

// TestGuildForCanalVazioNaoConsultaNada prova o guard de entrada: canal vazio
// devolve "" sem tocar no State nem no cache.
func TestGuildForCanalVazioNaoConsultaNada(t *testing.T) {
	s := &Sender{session: &discordgo.Session{State: discordgo.NewState()}}

	if got := s.guildFor(""); got != "" {
		t.Fatalf("esperava string vazia, veio %q", got)
	}
}
