package parser

import "testing"

// TestGUIDLineIndexPreenchidoNosOutrosRamosDeEmpty cobre os dois retornos
// Empty que a suíte não exercitava para GUIDLineIndex (drenagem 25/09/2026):
// arquivo truncado e linha 4 vazia. O campo é documentado como "preenchido em
// TODO retorno Empty"; sem este teste, trocar findGUIDLine(data) por 0 nesses
// dois ramos passava verde. Cada caso tem o par: GUID presente (índice > 0,
// para não coincidir com o zero value) e ausente (-1).
func TestGUIDLineIndexPreenchidoNosOutrosRamosDeEmpty(t *testing.T) {
	const guid = "*5416a6f4ea15c7a4782f4bf64dab0182*"
	cases := []struct {
		nome string
		data string
		idx  int
	}{
		{"truncado com GUID na linha 1", "BF4\n" + guid + " Nome\n", 1},
		{"truncado sem GUID", "BF4\nsvss\n", -1},
		{"linha 4 vazia com GUID na linha 5", "BF4\nsvss\nhost:1\n2026-06-09 18:50:49\n\n" + guid + " Nome\n", 5},
		{"linha 4 vazia sem GUID", "BF4\nsvss\nhost:1\n2026-06-09 18:50:49\n\nfim\n", -1},
	}
	for _, c := range cases {
		c := c
		t.Run(c.nome, func(t *testing.T) {
			info := Extract([]byte(c.data))
			if !info.Empty {
				t.Fatalf("deveria sinalizar Empty, veio %+v", info)
			}
			if info.GUIDLineIndex != c.idx {
				t.Fatalf("GUIDLineIndex: esperava %d, veio %d", c.idx, info.GUIDLineIndex)
			}
		})
	}
}
