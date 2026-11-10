package source

import (
	"testing"
	"time"
)

// TestIsScreenshot cobre o portão de entrada de todo o pipeline (usado em
// internal/queue/pipeline.go:97): decide o que é baixado, enviado e apagado
// da origem. Antes deste teste a função tinha zero cobertura.
func TestIsScreenshot(t *testing.T) {
	casos := []struct {
		nome     string
		arquivo  string
		tamanho  int64
		esperado bool
	}{
		{"tamanho normal", "pb000001.png", 2000, true},
		{"tamanho logo acima do limite", "pb000001.png", 1001, true},
		{"tamanho exatamente no limite (excludente)", "pb000001.png", 1000, false},
		{"maiusculo nao e reconhecido hoje", "pb000001.PNG", 2000, false},
		{"so a extensao, nome curto demais", ".png", 2000, false},
		{"nome minimo valido", "x.png", 2000, true},
		{"nome vazio", "", 2000, false},
		{"extensao errada", "pb000001.txt", 2000, false},
		{"extensao com sufixo extra", "pb000001.png.tmp", 2000, false},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			f := FileInfo{Name: c.arquivo, Size: c.tamanho, ModTime: time.Now()}
			if got := f.IsScreenshot(); got != c.esperado {
				t.Errorf("IsScreenshot(%q, size=%d) = %v, esperado %v", c.arquivo, c.tamanho, got, c.esperado)
			}
		})
	}
}
