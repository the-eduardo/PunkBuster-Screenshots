package config

import (
	"strings"
	"testing"
)

// TestLoad_ErroDeWaitingTimeERetentionTruncaValorEcoado estende a regra do
// AppSec de 12/09/2026 (TestLoad_ErroDeFtpModeTruncaValorEcoado) aos dois
// fail-closed novos de 25/09/2026: o erro de boot de WAITING_TIME e
// RETENTION_HOURS ecoa o valor recebido, e um segredo colado no campo errado
// do .env nao pode sair inteiro no log. Escrito na drenagem de 25/09/2026:
// sem ele, trocar truncateForError(raw) por raw nas duas mensagens passava
// com a suite verde.
func TestLoad_ErroDeWaitingTimeERetentionTruncaValorEcoado(t *testing.T) {
	segredo := "hunter2-token-supersecreto-que-nao-devia-vazar-no-log"
	for _, env := range []string{"WAITING_TIME", "RETENTION_HOURS"} {
		env := env
		t.Run(env, func(t *testing.T) {
			setRequiredEnv(t, map[string]string{env: segredo})
			_, err := Load()
			if err == nil {
				t.Fatalf("%s=<segredo> deveria falhar o boot", env)
			}
			if strings.Contains(err.Error(), segredo) {
				t.Fatalf("erro ecoou o valor completo de %s, deveria truncar: %v", env, err)
			}
			if !strings.Contains(err.Error(), env+`="`+segredo[:20]) {
				t.Fatalf("erro deveria nomear %s e manter um prefixo do valor pra diagnostico, veio: %v", env, err)
			}
		})
	}
}
