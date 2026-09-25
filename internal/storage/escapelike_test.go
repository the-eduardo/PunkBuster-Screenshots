package storage

import (
	"testing"
	"time"
)

// TestSearchByNameEscapaPercentEBarra completa a prova do escape de LIKE
// (drenagem 25/09/2026): TestSearchByNameEscapaCuringasDoLIKE só exercita o
// "_", então apagar o "%" ou a "\" do escapeLike passava com a suíte verde.
//   - "%": sem escape, "100%Bot" casaria "100xBot" (% = qualquer sequência).
//   - "\": é o caractere de ESCAPE da query; sem escapá-lo, o termo "a\b"
//     vira o padrão a-seguido-de-b-literal e casa "xaby", não "a\b".
func TestSearchByNameEscapaPercentEBarra(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()

	for _, rec := range []ScreenshotRecord{
		{GUID: "P1", PlayerName: "100%Bot", ReceivedAt: now, Server: "srv", FileName: "p1.png"},
		{GUID: "P2", PlayerName: "100xBot", ReceivedAt: now, Server: "srv", FileName: "p2.png"},
		{GUID: "S1", PlayerName: `a\b`, ReceivedAt: now, Server: "srv", FileName: "s1.png"},
		{GUID: "S2", PlayerName: "xaby", ReceivedAt: now, Server: "srv", FileName: "s2.png"},
	} {
		if err := s.RecordScreenshot(rec); err != nil {
			t.Fatalf("RecordScreenshot(%s) falhou: %v", rec.GUID, err)
		}
	}

	for _, c := range []struct{ termo, guid string }{
		{"100%Bot", "P1"},
		{`a\b`, "S1"},
	} {
		got, err := s.SearchByName(c.termo, 50)
		if err != nil {
			t.Fatalf("SearchByName(%q) falhou: %v", c.termo, err)
		}
		if len(got) != 1 || got[0].GUID != c.guid {
			t.Fatalf("SearchByName(%q): esperava só %s (literal), veio %+v", c.termo, c.guid, got)
		}
	}
}
