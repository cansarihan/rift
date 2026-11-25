package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cansarihan/rift/internal/diff"
)

func sample() diff.Summary {
	return diff.Summary{
		Old: "old", New: "new", Arch: "arm64",
		Compared: 100, Modified: 2, Added: 1, Removed: 0, Flagged: 1,
		Counts: map[string]int{diff.BandLikely: 1, diff.BandPossible: 0, diff.BandUnlikely: 2},
		Index:  61, Band: diff.BandLikely,
		Deltas: []diff.FuncDelta{
			{Name: "main.parse", Status: diff.StatusModified, Score: 61, Band: diff.BandLikely, Reasons: []string{"adds an error/validation path (fmt.errorf)"}},
			{Name: "runtime.leaf", Status: diff.StatusRemoved, Score: 6, Band: diff.BandUnlikely},
		},
	}
}

func TestTable(t *testing.T) {
	var buf bytes.Buffer
	Table(&buf, sample())
	out := buf.String()
	for _, want := range []string{"rift likely", "main.parse", "error/validation path", "BAND", "SCORE"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q\n%s", want, out)
		}
	}
	// A removed, unlikely entry is suppressed from the table.
	if strings.Contains(out, "runtime.leaf") {
		t.Errorf("table should not list the unlikely removed function\n%s", out)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, sample()); err != nil {
		t.Fatal(err)
	}
	var back diff.Summary
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if back.Band != diff.BandLikely || len(back.Deltas) != 2 {
		t.Errorf("round trip lost data: %+v", back)
	}
}

func TestTableEmpty(t *testing.T) {
	var buf bytes.Buffer
	s := sample()
	s.Deltas = nil
	Table(&buf, s)
	if !strings.Contains(buf.String(), "no differences") {
		t.Errorf("empty table should say so:\n%s", buf.String())
	}
}
