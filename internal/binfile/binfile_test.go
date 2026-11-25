package binfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// buildSample compiles one of the demo sample programs to a temp file so the
// real format readers run against a genuine Go binary on the test platform.
func buildSample(t *testing.T, pkg string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping build-backed test in short mode")
	}
	out := filepath.Join(t.TempDir(), "sample")
	cmd := exec.Command("go", "build", "-o", out, "github.com/cansarihan/rift/deploy/sample/"+pkg)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("go build failed: %v\n%s", err, output)
	}
	return out
}

func TestResolveAndLookup(t *testing.T) {
	img := New("test", ArchAMD64, []Func{
		{Name: "b", Addr: 0x2000, Size: 0x100, Code: make([]byte, 0x100)},
		{Name: "a", Addr: 0x1000, Size: 0x40, Code: make([]byte, 0x40)},
	})

	// Functions are sorted by address regardless of input order.
	if img.Funcs[0].Name != "a" || img.Funcs[1].Name != "b" {
		t.Fatalf("functions not sorted by address: %+v", img.Funcs)
	}

	if _, ok := img.Lookup("a"); !ok {
		t.Fatal("Lookup(a) missing")
	}
	if _, ok := img.Lookup("missing"); ok {
		t.Fatal("Lookup(missing) should fail")
	}

	cases := []struct {
		addr uint64
		want string
		ok   bool
	}{
		{0x1000, "a", true},
		{0x103f, "a", true},
		{0x1040, "", false}, // past the end of a, before b
		{0x2000, "b", true},
		{0x2100, "", false}, // past the end of b
		{0x500, "", false},  // before everything
	}
	for _, c := range cases {
		got, ok := img.Resolve(c.addr)
		if ok != c.ok || got != c.want {
			t.Errorf("Resolve(%#x) = (%q,%v), want (%q,%v)", c.addr, got, ok, c.want, c.ok)
		}
	}
}

func TestBuildUsesGapWhenSizeMissing(t *testing.T) {
	text := make([]byte, 0x80)
	syms := []symbol{
		{name: "one", addr: 0x100},
		{name: "two", addr: 0x140},
	}
	funcs := build(0x100, text, syms)
	if len(funcs) != 2 {
		t.Fatalf("want 2 funcs, got %d", len(funcs))
	}
	if funcs[0].Size != 0x40 {
		t.Errorf("first func size = %#x, want 0x40 (gap to next)", funcs[0].Size)
	}
	if funcs[1].Size != 0x40 {
		t.Errorf("second func size = %#x, want 0x40 (gap to text end)", funcs[1].Size)
	}
}

// Opening a freshly built sample exercises the real format readers end to end.
func TestOpenRealBinary(t *testing.T) {
	img, err := Open(buildSample(t, "old"))
	if err != nil {
		t.Fatal(err)
	}
	if len(img.Funcs) == 0 {
		t.Fatal("no functions recovered from the binary")
	}
	if img.Arch != ArchAMD64 && img.Arch != ArchARM64 {
		t.Logf("unusual arch %q", img.Arch)
	}
	if _, ok := img.Lookup("main.main"); !ok {
		t.Error("expected main.main in the binary")
	}
	if _, ok := img.Lookup("main.parseRecord"); !ok {
		t.Error("expected main.parseRecord in the binary")
	}
}

func TestOpenRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/junk"
	if err := os.WriteFile(path, []byte("not a binary at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("expected an error opening a non-binary file")
	}
}
