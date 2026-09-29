package main

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
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
