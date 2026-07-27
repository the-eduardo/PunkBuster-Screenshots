package source

import (
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Chave real do servidor de screenshots (pedro.fragify.net:2025), coletada por
// ssh-keyscan de tres pontos de rede independentes em 27/07/2026, todos com a
// mesma fingerprint SHA256:5LzaJvjX8SVANOvXQMFAlEjVuDpSRRRJ5D3Gaakwyv4.
const chaveServidor = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHU9IJm2mkA+qUTfkbbfOIkN0cRpk7Bvsiq4J0q18UbT"

// Linha completa como o ssh-keyscan imprime, com o prefixo de host e porta:
// e' esse texto que se copia e cola na pratica.
const linhaKeyscan = "[pedro.fragify.net]:2025 " + chaveServidor

// Outra chave valida qualquer, para simular um servidor diferente do esperado.
const chaveIntrusa = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKj8Zt0kfQZTVdKKvI2fjK5wZ3nQXoJZlZ8eHrJvVdMt"

func pubDe(t *testing.T, linha string) ssh.PublicKey {
	t.Helper()
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(linha))
	if err != nil {
		t.Fatalf("fixture invalida %q: %v", linha, err)
	}
	return pub
}

func TestParseHostKey_AceitaOsDoisFormatos(t *testing.T) {
	casos := map[string]string{
		"so a chave":          chaveServidor,
		"linha do ssh-keyscan": linhaKeyscan,
		"com espaco em volta":  "  " + linhaKeyscan + "  ",
	}
	esperada := ssh.FingerprintSHA256(pubDe(t, chaveServidor))

	for nome, entrada := range casos {
		t.Run(nome, func(t *testing.T) {
			pub, err := parseHostKey(entrada)
			if err != nil {
				t.Fatalf("nao devia falhar: %v", err)
			}
			if got := ssh.FingerprintSHA256(pub); got != esperada {
				t.Errorf("fingerprint = %s, esperada %s", got, esperada)
			}
		})
	}
}

func TestParseHostKey_RejeitaLixo(t *testing.T) {
	for _, entrada := range []string{"", "ssh-ed25519", "nao-e-chave", "ssh-ed25519 !!!nao-base64!!!"} {
		if _, err := parseHostKey(entrada); err == nil {
			t.Errorf("deveria falhar para %q", entrada)
		}
	}
}

// O ponto central da mudanca: sem chave e sem opt-out explicito, o bot nao sobe.
func TestHostKeyCallback_SemChaveFalhaFechado(t *testing.T) {
	_, err := hostKeyCallback("", false)
	if err == nil {
		t.Fatal("sem SFTP_HOST_KEY e sem opt-out, deveria falhar")
	}
	if !strings.Contains(err.Error(), "SFTP_HOST_KEY") {
		t.Errorf("erro deveria citar a variavel a definir, veio: %v", err)
	}
}

func TestHostKeyCallback_OptOutExplicitoPassa(t *testing.T) {
	cb, err := hostKeyCallback("", true)
	if err != nil {
		t.Fatalf("opt-out explicito nao deveria falhar: %v", err)
	}
	if cb(":2025", &net.TCPAddr{}, pubDe(t, chaveIntrusa)) != nil {
		t.Error("com opt-out, qualquer chave deveria ser aceita")
	}
}

func TestHostKeyCallback_AceitaChaveCerta(t *testing.T) {
	cb, err := hostKeyCallback(linhaKeyscan, false)
	if err != nil {
		t.Fatalf("nao devia falhar: %v", err)
	}
	if err := cb("pedro.fragify.net:2025", &net.TCPAddr{}, pubDe(t, chaveServidor)); err != nil {
		t.Errorf("a chave correta deveria passar, veio: %v", err)
	}
}

func TestHostKeyCallback_RejeitaChaveTrocadaEMostraAsDuasFingerprints(t *testing.T) {
	cb, err := hostKeyCallback(chaveServidor, false)
	if err != nil {
		t.Fatalf("nao devia falhar: %v", err)
	}
	intrusa := pubDe(t, chaveIntrusa)

	err = cb("pedro.fragify.net:2025", &net.TCPAddr{}, intrusa)
	if err == nil {
		t.Fatal("chave diferente deveria ser rejeitada")
	}
	// O erro precisa ser acionavel: se a troca for legitima, o operador ja le a
	// nova fingerprint no log em vez de ter que ir buscar.
	if !strings.Contains(err.Error(), ssh.FingerprintSHA256(intrusa)) {
		t.Errorf("erro deveria trazer a fingerprint recebida, veio: %v", err)
	}
	if !strings.Contains(err.Error(), ssh.FingerprintSHA256(pubDe(t, chaveServidor))) {
		t.Errorf("erro deveria trazer a fingerprint esperada, veio: %v", err)
	}
}
