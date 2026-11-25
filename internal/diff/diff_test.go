package diff

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/cansarihan/rift/internal/binfile"
)

// caller builds an amd64 function whose code is a run of direct calls to the
// given target addresses, padded to size with NOPs.
func caller(name string, addr uint64, size int, callees ...uint64) binfile.Func {
	code := make([]byte, size)
	for i := range code {
		code[i] = 0x90
	}
	off := 0
	for _, c := range callees {
		code[off] = 0xe8
		rel := int32(int64(c) - int64(addr+uint64(off)+5))
		binary.LittleEndian.PutUint32(code[off+1:off+5], uint32(rel))
		off += 5
	}
	return binfile.Func{Name: name, Addr: addr, Size: uint64(size), Code: code}
}

func leaf(name string, addr uint64) binfile.Func {
	return binfile.Func{Name: name, Addr: addr, Size: 16, Code: make([]byte, 16)}
}

func findDelta(s Summary, name string) (FuncDelta, bool) {
	for _, d := range s.Deltas {
		if d.Name == name {
			return d, true
		}
	}
	return FuncDelta{}, false
}

func TestRunGradesChanges(t *testing.T) {
	const helper, errorf, guard = 0x3000, 0x3100, 0x3200

	base := binfile.New("t", binfile.ArchAMD64, []binfile.Func{
		caller("main.parse", 0x1000, 20, helper),
		caller("main.gone", 0x1800, 16, helper),
		caller("main.stable", 0x1c00, 20, helper),
		leaf("other.helper", helper),
		leaf("fmt.errorf", errorf),
		leaf("runtime.panicBounds", guard),
	})

	updated := binfile.New("t", binfile.ArchAMD64, []binfile.Func{
		caller("main.parse", 0x1000, 40, helper, errorf), // grew + new error path
		caller("main.newfn", 0x1500, 20, guard, errorf),  // added validation helper
		caller("main.stable", 0x1c00, 20, helper),        // unchanged
		leaf("other.helper", helper),
		leaf("fmt.errorf", errorf),
		leaf("runtime.panicBounds", guard),
	})

	summary, err := Run(context.Background(), base, updated, nil)
	if err != nil {
		t.Fatal(err)
	}

	parse, ok := findDelta(summary, "main.parse")
	if !ok {
		t.Fatal("main.parse not reported")
	}
	if parse.Status != StatusModified {
		t.Errorf("main.parse status = %q, want modified", parse.Status)
	}
	if parse.Band != BandLikely && parse.Band != BandPossible {
		t.Errorf("main.parse band = %q, want likely/possible", parse.Band)
	}
	if !strings.Contains(strings.Join(parse.Reasons, " "), "error/validation path") {
		t.Errorf("main.parse reasons missing error path: %v", parse.Reasons)
	}

	newfn, ok := findDelta(summary, "main.newfn")
	if !ok || newfn.Status != StatusAdded {
		t.Errorf("main.newfn not reported as added: %+v", newfn)
	}

	gone, ok := findDelta(summary, "main.gone")
	if !ok || gone.Status != StatusRemoved {
		t.Errorf("main.gone not reported as removed: %+v", gone)
	}

	if _, ok := findDelta(summary, "main.stable"); ok {
		t.Error("main.stable is unchanged and should not be reported")
	}

	if summary.Band != BandLikely && summary.Band != BandPossible {
		t.Errorf("overall band = %q, want likely/possible", summary.Band)
	}
	if summary.Flagged < 1 {
		t.Errorf("expected at least one flagged function, got %d", summary.Flagged)
	}
}

// A function whose only difference is a relocated call target (same callee name,
// same size) must be treated as unchanged once displacements are masked.
func TestRunIgnoresRelocationNoise(t *testing.T) {
	base := binfile.New("t", binfile.ArchAMD64, []binfile.Func{
		caller("main.f", 0x1000, 20, 0x3000),
		leaf("other.helper", 0x3000),
	})
	updated := binfile.New("t", binfile.ArchAMD64, []binfile.Func{
		caller("main.f", 0x1000, 20, 0x4000), // helper moved; same name
		leaf("other.helper", 0x4000),
	})

	summary, err := Run(context.Background(), base, updated, nil)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Modified != 0 {
		t.Errorf("relocation-only change counted as modified: %d", summary.Modified)
	}
	if _, ok := findDelta(summary, "main.f"); ok {
		t.Error("main.f should be filtered as layout noise")
	}
	if summary.Band != BandClean {
		t.Errorf("band = %q, want clean", summary.Band)
	}
}

func TestChurnBounds(t *testing.T) {
	a := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if c := churn(a, a); c != 0 {
		t.Errorf("churn(a,a) = %v, want 0", c)
	}
	b := []byte{100, 99, 98, 97, 96, 95, 94, 93, 92, 91}
	if c := churn(a, b); c <= 0.9 {
		t.Errorf("churn of disjoint code = %v, want near 1", c)
	}
}

func TestGuardAndErrorClassification(t *testing.T) {
	guards := []string{"runtime.panicBounds", "runtime.goPanicIndex", "fmt.errorf", "other.helper"}
	g := guardsAmong(guards)
	if len(g) != 2 {
		t.Errorf("guardsAmong = %v, want 2 runtime guards", g)
	}
	if !isErrorPath("fmt.errorf") || !isErrorPath("errors.New") || !isErrorPath("x.Error") {
		t.Error("error path classifier missed a known constructor")
	}
	if isErrorPath("runtime.panicBounds") {
		t.Error("panic must not be classified as an error path")
	}
}
