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
		"SERVER":      "example.com",
		"USER":        "user",
		"PASS":        "pass",
		"SFTP_FOLDER": "/folder",
		"BOT_TOKEN":   "token",
		"CHANNEL_ID":  "123",
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

func TestLoad_SelectFTPModeDefaultParaFTP(t *testing.T) {
	cases := []string{"", "invalido", "SFTP_TYPO"}
	for _, mode := range cases {
		mode := mode
		t.Run("mode="+mode, func(t *testing.T) {
			setRequiredEnv(t, map[string]string{"SELECT_FTP_MODE": mode})
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() não deveria falhar com SELECT_FTP_MODE=%q, erro: %v", mode, err)
			}
			if cfg.SelectFTPMode != "ftp" {
				t.Fatalf("SelectFTPMode = %q, esperado default \"ftp\"", cfg.SelectFTPMode)
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
