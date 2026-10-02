package main

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// dockerDefaultStopTimeout é o grace que o Docker aplica quando o compose não
// declara stop_grace_period.
const dockerDefaultStopTimeout = 10 * time.Second

// composeStopGrace lê o stop_grace_period do serviço server1 no
// docker-compose.yml do repo. Parser de linha proposital (sem dependência de
// YAML): o bloco do serviço vai de "  server1:" até a próxima chave com a
// mesma indentação ou menor.
func composeStopGrace(t *testing.T) (time.Duration, bool) {
	t.Helper()
	f, err := os.Open("../../docker-compose.yml")
	if err != nil {
		t.Fatalf("abrir docker-compose.yml: %v", err)
	}
	defer f.Close()

	inServer1 := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if line == "  server1:" {
			inServer1 = true
			continue
		}
		if inServer1 && indent <= 2 {
			break // saiu do bloco do server1
		}
		if inServer1 && indent == 4 && strings.HasPrefix(trimmed, "stop_grace_period:") {
			v := strings.TrimSpace(strings.TrimPrefix(trimmed, "stop_grace_period:"))
			if i := strings.Index(v, "#"); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				t.Fatalf("stop_grace_period %q não é duração válida: %v", v, err)
			}
			return d, true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("ler docker-compose.yml: %v", err)
	}
	return 0, false
}

// O prazo que o processo dá ao Sender tem que caber no grace do compose (senão
// o SIGKILL chega antes e o resto do shutdown morre no meio) E usar esse grace
// de verdade: com o Close antigo de 8s, o stop_grace_period de 30s ficava
// inerte, porque o processo desistia da fila 22s antes do Docker matar.
func TestSenderCloseTimeoutCasaComStopGraceDoCompose(t *testing.T) {
	grace, ok := composeStopGrace(t)
	if !ok {
		t.Fatalf("server1 sem stop_grace_period no docker-compose.yml: vale o default de %v do Docker, pouco pro Sender drenar", dockerDefaultStopTimeout)
	}
	if grace <= dockerDefaultStopTimeout {
		t.Fatalf("stop_grace_period = %v, precisa passar do default de %v do Docker", grace, dockerDefaultStopTimeout)
	}
	folga := grace - senderCloseTimeout
	if folga < 5*time.Second {
		t.Errorf("senderCloseTimeout = %v deixa só %v de folga sob o stop_grace_period de %v; mínimo 5s pro resto do shutdown", senderCloseTimeout, folga, grace)
	}
	if folga > 10*time.Second {
		t.Errorf("senderCloseTimeout = %v desperdiça %v do stop_grace_period de %v: o grace fica inerte (o processo desiste da fila antes do Docker matar)", senderCloseTimeout, folga, grace)
	}
}

// Fiação: a constante só vale se é ela que main() passa pro sender.Close.
// Um literal no call site (ex.: o 8*time.Second antigo) passaria no teste de
// cima sem mudar nada em produção.
func TestMainPassaSenderCloseTimeoutProSenderClose(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	var calls, comConstante int
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Close" {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || recv.Name != "sender" {
			return true
		}
		calls++
		if len(call.Args) == 1 {
			if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == "senderCloseTimeout" {
				comConstante++
			}
		}
		return true
	})
	if calls == 0 {
		t.Fatal("main.go não chama sender.Close: a fila do Sender não drena no shutdown")
	}
	if comConstante != calls {
		t.Fatalf("main.go chama sender.Close %d vez(es), só %d com senderCloseTimeout", calls, comConstante)
	}
}

// DEBUG_MODE só tem efeito vivo no repo através do nível do handler de log —
// até 02/10/2026 não havia nenhum slog.Debug consumindo esse nível, e o único
// efeito real da variável era poder abortar o boot (fail-closed de 28/09).
func TestLogLevel(t *testing.T) {
	if got := logLevel(true); got != slog.LevelDebug {
		t.Errorf("logLevel(true) = %v, esperado %v", got, slog.LevelDebug)
	}
	if got := logLevel(false); got != slog.LevelInfo {
		t.Errorf("logLevel(false) = %v, esperado %v", got, slog.LevelInfo)
	}
}

// Fiação: logLevel só vale se é ela que decide o Level do handler em main().
// Um literal no call site (ex.: slog.LevelInfo direto, ignorando
// cfg.DebugMode) passaria em TestLogLevel sem conectar DEBUG_MODE a nada.
func TestMainUsaLogLevelParaNivelDoHandler(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	achou := false
	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Level" {
			return true
		}
		call, ok := kv.Value.(*ast.CallExpr)
		if !ok {
			return true
		}
		if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "logLevel" {
			achou = true
		}
		return true
	})
	if !achou {
		t.Fatal("main.go não usa logLevel(...) pra montar o Level do handler — DEBUG_MODE pode ter ficado desconectado do nível de log")
	}
}
