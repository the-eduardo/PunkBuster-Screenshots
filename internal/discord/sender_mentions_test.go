package discord

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// fakeMentionsTransport captura o request feito por send() sem tocar rede: o
// RoundTripper intercepta antes de qualquer resolução de DNS/conexão.
type fakeMentionsTransport struct {
	t       *testing.T
	payload map[string]any
	gotFile bool
}

func (f *fakeMentionsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	mediaType, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		f.t.Fatalf("esperava multipart/form-data (envio com arquivo), content-type=%q err=%v", req.Header.Get("Content-Type"), err)
	}

	mr := multipart.NewReader(req.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			f.t.Fatalf("erro lendo parte do multipart: %v", err)
		}
		switch part.FormName() {
		case "payload_json":
			data, err := io.ReadAll(part)
			if err != nil {
				f.t.Fatalf("erro lendo payload_json: %v", err)
			}
			if err := json.Unmarshal(data, &f.payload); err != nil {
				f.t.Fatalf("payload_json não é JSON válido: %v (%s)", err, data)
			}
		case "files[0]":
			f.gotFile = true
		}
	}

	body := bytes.NewBufferString(`{"id":"1"}`)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(body),
		Header:     make(http.Header),
	}, nil
}

// TestSendBloqueiaMencoesDoNomeDoJogador prova que send() manda
// allowed_mentions.parse como array VAZIO — se PlayerName vier com
// "@everyone"/"<@id>" (nome vem cru do header do arquivo, pbheader.go:153),
// o Discord não deve interpretar como menção. O conteúdo continua trazendo o
// nome (não é censura, é supressão de interpretação) e o arquivo segue anexado.
func TestSendBloqueiaMencoesDoNomeDoJogador(t *testing.T) {
	fake := &fakeMentionsTransport{t: t}
	session, err := discordgo.New("Bot token-fake")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	session.Client.Transport = fake

	tmp := filepath.Join(t.TempDir(), "pb000001.png")
	if err := os.WriteFile(tmp, []byte("fake-png"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := &Sender{session: session}
	_, err = s.send(SendJob{
		ChannelID:  "123",
		LocalPath:  tmp,
		FileName:   "pb000001.png",
		GUID:       "abc",
		PlayerName: "@everyone <@456>",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	if !fake.gotFile {
		t.Fatal("FIACAO VAZIA: o arquivo não foi anexado ao multipart")
	}

	content, _ := fake.payload["content"].(string)
	if !bytes.Contains([]byte(content), []byte("@everyone <@456>")) {
		t.Fatalf("o nome do jogador deveria continuar no content (suprimir é só a INTERPRETAÇÃO da menção, não o texto); content=%q", content)
	}

	am, ok := fake.payload["allowed_mentions"].(map[string]any)
	if !ok {
		t.Fatalf("FIACAO VAZIA: allowed_mentions ausente do payload — sem isso o Discord usa o default (mencionar a partir do content); payload=%v", fake.payload)
	}
	parse, ok := am["parse"].([]any)
	if !ok {
		t.Fatalf("allowed_mentions.parse ausente ou no formato errado: %v", am["parse"])
	}
	if len(parse) != 0 {
		t.Fatalf("allowed_mentions.parse deveria ser array vazio (bloqueia TODAS as menções), veio: %v", parse)
	}
}
