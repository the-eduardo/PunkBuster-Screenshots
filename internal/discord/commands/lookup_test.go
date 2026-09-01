package commands

import (
	"path/filepath"
	"testing"

	"pbss/internal/storage"
)

// TestLookupAchaGUIDGravadoComAsterisco exercita a fiação real: lookup é o
// ponto exato onde guidPattern decide qual método do store roda. O
// PunkBuster grava o GUID com asteriscos nas pontas (parser/pbheader.go
// copia parts[0] cru), formato que nenhuma fixture antiga usava — por isso
// esse defeito sobreviveu à suíte. Os dois jeitos de o usuário colar o termo
// (como o bot exibe, com asterisco; ou só o hex) têm que achar o resultado.
func TestLookupAchaGUIDGravadoComAsterisco(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open falhou: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	const guidComAsterisco = "*5416a6f4ea15c7a4782f4bf64dab0182*"
	const guidHex = "5416a6f4ea15c7a4782f4bf64dab0182"
	if err := store.RecordScreenshot(storage.ScreenshotRecord{
		GUID: guidComAsterisco, PlayerName: "JoseToalha", FileName: "pb007647.png", Server: "srv",
	}); err != nil {
		t.Fatalf("RecordScreenshot falhou: %v", err)
	}

	h := &Handler{Store: store, states: make(map[string]*searchState)}

	casos := []struct {
		nome  string
		termo string
	}{
		{"como o bot exibe (com asterisco)", guidComAsterisco},
		{"so o hex", guidHex},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			results, err := h.lookup(c.termo, 10)
			if err != nil {
				t.Fatalf("lookup(%q) falhou: %v", c.termo, err)
			}
			if len(results) != 1 || results[0].PlayerName != "JoseToalha" {
				t.Errorf("lookup(%q) = %+v, esperava 1 resultado de JoseToalha", c.termo, results)
			}
		})
	}
}
