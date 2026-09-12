package config

import (
	"strings"
	"testing"
	"time"
)

// setRequiredEnv preenche as 6 variáveis obrigatórias com valores válidos e
// aplica overrides por cima, via t.Setenv (que restaura o valor original
// depois do teste, então cada caso fica isolado do resto do arquivo).
func setRequiredEnv(t *testing.T, overrides map[string]string) {
	t.Helper()
	base := map[string]string{
		"SERVER":          "example.com",
		"USER":            "user",
		"PASS":            "pass",
		"SFTP_FOLDER":     "/folder",
		"BOT_TOKEN":       "token",
		"CHANNEL_ID":      "123",
		"SELECT_FTP_MODE": "ftp",
	}
	for k, v := range base {
		if ov, ok := overrides[k]; ok {
			v = ov
		}
		t.Setenv(k, v)
	}
	for k, v := range overrides {
		if _, ok := base[k]; !ok {
			t.Setenv(k, v)
		}
	}
}

func TestLoad_MissingRequiredVar(t *testing.T) {
	required := []string{"SERVER", "USER", "PASS", "SFTP_FOLDER", "BOT_TOKEN", "CHANNEL_ID"}
	for _, name := range required {
		name := name
		t.Run(name, func(t *testing.T) {
			setRequiredEnv(t, map[string]string{name: ""})
			_, err := Load()
			if err == nil {
				t.Fatalf("esperava erro por falta de %s, mas Load() passou", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("erro deveria citar %s, veio: %v", name, err)
			}
		})
	}
}

func TestLoad_SFTPModeFailsClosedSemHostKey(t *testing.T) {
	setRequiredEnv(t, map[string]string{
		"SELECT_FTP_MODE":        "sftp",
		"SFTP_HOST_KEY":          "",
		"SFTP_INSECURE_HOST_KEY": "",
	})
	_, err := Load()
	if err == nil {
		t.Fatalf("modo sftp sem SFTP_HOST_KEY e sem bypass explicito deveria falhar (fail-closed)")
	}
}

func TestLoad_SFTPModeComInsecureBypassPassa(t *testing.T) {
	setRequiredEnv(t, map[string]string{
		"SELECT_FTP_MODE":        "sftp",
		"SFTP_HOST_KEY":          "",
		"SFTP_INSECURE_HOST_KEY": "true",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("SFTP_INSECURE_HOST_KEY=true deveria liberar o bypass explicito, erro: %v", err)
	}
	if cfg.SelectFTPMode != "sftp" {
		t.Fatalf("SelectFTPMode = %q, esperado \"sftp\"", cfg.SelectFTPMode)
	}
	if !cfg.SFTPInsecureHostKey {
		t.Fatalf("SFTPInsecureHostKey deveria ser true")
	}
}

func TestLoad_SelectFTPModeDesconhecidoFalha(t *testing.T) {
	cases := []string{"", "invalido", "SFTP_TYPO"}
	for _, mode := range cases {
		mode := mode
		t.Run("mode="+mode, func(t *testing.T) {
			setRequiredEnv(t, map[string]string{"SELECT_FTP_MODE": mode})
			_, err := Load()
			if err == nil {
				t.Fatalf("SELECT_FTP_MODE=%q deveria falhar o boot (fail-closed), mas Load() passou", mode)
			}
		})
	}
}

// TestLoad_ErroDeFtpModeTruncaValorEcoado e' o ajuste do AppSec (comite de
// 12/09/2026): se alguem colar um segredo por engano em SELECT_FTP_MODE, a
// mensagem de erro do fail-closed nao pode devolver o valor inteiro em texto
// claro no log de boot.
func TestLoad_ErroDeFtpModeTruncaValorEcoado(t *testing.T) {
	segredo := "hunter2-token-supersecreto-que-nao-devia-vazar-no-log"
	setRequiredEnv(t, map[string]string{"SELECT_FTP_MODE": segredo})
	_, err := Load()
	if err == nil {
		t.Fatalf("deveria falhar o boot")
	}
	if strings.Contains(err.Error(), segredo) {
		t.Fatalf("erro ecoou o valor completo de SELECT_FTP_MODE, deveria truncar: %v", err)
	}
	if !strings.Contains(err.Error(), segredo[:20]) {
		t.Fatalf("erro deveria manter um prefixo do valor pra diagnostico, veio: %v", err)
	}
}

// TestLoad_TypoNaoBypassaGuardaDeHostKey e' o teste de fiacao: um typo em
// SELECT_FTP_MODE nao pode mais degradar silenciosamente para "ftp" e, de
// quebra, pular a guarda fail-closed de SFTP_HOST_KEY.
func TestLoad_TypoNaoBypassaGuardaDeHostKey(t *testing.T) {
	setRequiredEnv(t, map[string]string{
		"SELECT_FTP_MODE":        "sfpt",
		"SFTP_HOST_KEY":          "",
		"SFTP_INSECURE_HOST_KEY": "",
	})
	_, err := Load()
	if err == nil {
		t.Fatalf("SELECT_FTP_MODE=\"sfpt\" (typo) deveria falhar o boot, nao cair em ftp sem guarda")
	}
}

func TestLoad_ModosValidosPassam(t *testing.T) {
	cases := []string{"sftp", "ftp", "SFTP"}
	for _, mode := range cases {
		mode := mode
		t.Run("mode="+mode, func(t *testing.T) {
			setRequiredEnv(t, map[string]string{
				"SELECT_FTP_MODE": mode,
				"SFTP_HOST_KEY":   "ssh-ed25519 AAAAtest",
			})
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() não deveria falhar com SELECT_FTP_MODE=%q, erro: %v", mode, err)
			}
			if cfg.SelectFTPMode != strings.ToLower(mode) {
				t.Fatalf("SelectFTPMode = %q, esperado %q", cfg.SelectFTPMode, strings.ToLower(mode))
			}
		})
	}
}

func TestLoad_WaitingTimeDefaultForaDoIntervalo(t *testing.T) {
	cases := []string{"", "1", "121", "abc"}
	for _, wt := range cases {
		wt := wt
		t.Run("waiting_time="+wt, func(t *testing.T) {
			setRequiredEnv(t, map[string]string{"WAITING_TIME": wt})
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() não deveria falhar com WAITING_TIME=%q, erro: %v", wt, err)
			}
			if cfg.WaitingTime != 30*time.Minute {
				t.Fatalf("WaitingTime = %v, esperado default 30m para WAITING_TIME=%q", cfg.WaitingTime, wt)
			}
		})
	}
}

func TestLoad_WaitingTimeValidoDentroDoIntervalo(t *testing.T) {
	setRequiredEnv(t, map[string]string{"WAITING_TIME": "45"})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() falhou com WAITING_TIME válido: %v", err)
	}
	if cfg.WaitingTime != 45*time.Minute {
		t.Fatalf("WaitingTime = %v, esperado 45m", cfg.WaitingTime)
	}
}

func TestLoad_RetentionHoursDefaultQuandoInvalido(t *testing.T) {
	cases := []string{"", "0", "-5", "abc"}
	for _, rh := range cases {
		rh := rh
		t.Run("retention_hours="+rh, func(t *testing.T) {
			setRequiredEnv(t, map[string]string{"RETENTION_HOURS": rh})
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() não deveria falhar com RETENTION_HOURS=%q, erro: %v", rh, err)
			}
			if cfg.RetentionHours != 24 {
				t.Fatalf("RetentionHours = %d, esperado default 24 para RETENTION_HOURS=%q", cfg.RetentionHours, rh)
			}
		})
	}
}

func TestLoad_RetentionHoursValido(t *testing.T) {
	setRequiredEnv(t, map[string]string{"RETENTION_HOURS": "72"})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() falhou com RETENTION_HOURS válido: %v", err)
	}
	if cfg.RetentionHours != 72 {
		t.Fatalf("RetentionHours = %d, esperado 72", cfg.RetentionHours)
	}
}
