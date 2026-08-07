package commands

import (
	"testing"
	"time"
)

func TestPurgeExpiredStatesRemovesOnlyOldEntries(t *testing.T) {
	h := &Handler{states: make(map[string]*searchState)}

	h.states["old"] = &searchState{query: "old", createdAt: time.Now().Add(-20 * time.Minute)}
	h.states["fresh"] = &searchState{query: "fresh", createdAt: time.Now()}

	h.PurgeExpiredStates(15 * time.Minute)

	if _, ok := h.states["old"]; ok {
		t.Error("estado com mais de maxAge deveria ter sido removido")
	}
	if _, ok := h.states["fresh"]; !ok {
		t.Error("estado recente nao deveria ter sido removido")
	}
}

func TestPurgeExpiredStatesKeepsManyFreshEntries(t *testing.T) {
	h := &Handler{states: make(map[string]*searchState)}

	for i := 0; i < 600; i++ {
		h.states[string(rune(i))] = &searchState{createdAt: time.Now()}
	}

	h.PurgeExpiredStates(15 * time.Minute)

	if len(h.states) != 600 {
		t.Errorf("esperava manter as 600 entradas recentes (mesmo acima do antigo cap de 500), tinha %d", len(h.states))
	}
}
