package callscan

import (
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/cansarihan/rift/internal/binfile"
)

func TestAMD64Callee(t *testing.T) {
	// nop; call rel32 -> 0x2000
	code := []byte{0x90, 0xe8, 0, 0, 0, 0}
	caller := binfile.Func{Name: "caller", Addr: 0x1000, Size: uint64(len(code)), Code: code}
	rel := int32(0x2000 - (0x1000 + 1 + 5))
	binary.LittleEndian.PutUint32(code[2:6], uint32(rel))

	img := binfile.New("t", binfile.ArchAMD64, []binfile.Func{
		caller,
		{Name: "callee", Addr: 0x2000, Size: 0x10, Code: make([]byte, 0x10)},
	})

	got := Callees(binfile.ArchAMD64, caller, img)
	if !reflect.DeepEqual(got, []string{"callee"}) {
		t.Fatalf("Callees = %v, want [callee]", got)
	}
}

func TestARM64Callee(t *testing.T) {
	// BL +0x40  (imm26 = 0x10 instructions)
	word := uint32(0b100101)<<26 | 0x10
	code := make([]byte, 4)
	binary.LittleEndian.PutUint32(code, word)
	caller := binfile.Func{Name: "caller", Addr: 0x1000, Size: 4, Code: code}

	img := binfile.New("t", binfile.ArchARM64, []binfile.Func{
		caller,
		{Name: "callee", Addr: 0x1040, Size: 0x10, Code: make([]byte, 0x10)},
	})

	got := Callees(binfile.ArchARM64, caller, img)
	if !reflect.DeepEqual(got, []string{"callee"}) {
		t.Fatalf("Callees = %v, want [callee]", got)
	}
}

func TestUnresolvedTargetDropped(t *testing.T) {
	code := []byte{0xe8, 0, 0, 0, 0}
	caller := binfile.Func{Name: "caller", Addr: 0x1000, Size: uint64(len(code)), Code: code}
	binary.LittleEndian.PutUint32(code[1:5], uint32(int32(0x9000))) // lands nowhere
	img := binfile.New("t", binfile.ArchAMD64, []binfile.Func{caller})
	if got := Callees(binfile.ArchAMD64, caller, img); len(got) != 0 {
		t.Fatalf("expected no resolved callees, got %v", got)
	}
}

func TestNormalizeAMD64ZeroesDisplacement(t *testing.T) {
	code := []byte{0x90, 0xe8, 0x11, 0x22, 0x33, 0x44}
	fn := binfile.Func{Addr: 0x1000, Size: uint64(len(code)), Code: code}
	norm := NormalizedCode(binfile.ArchAMD64, fn)
	for i := 2; i < 6; i++ {
		if norm[i] != 0 {
			t.Errorf("displacement byte %d not zeroed: %#x", i, norm[i])
		}
	}
	// The original is untouched.
	if code[2] != 0x11 {
		t.Error("NormalizedCode mutated the input code")
	}
}

func TestNormalizeARM64MasksBranch(t *testing.T) {
	word := uint32(0b100101)<<26 | 0x12345
	code := make([]byte, 4)
	binary.LittleEndian.PutUint32(code, word)
	fn := binfile.Func{Addr: 0x1000, Size: 4, Code: code}
	norm := NormalizedCode(binfile.ArchARM64, fn)
	got := binary.LittleEndian.Uint32(norm)
	if got != uint32(0b100101)<<26 {
		t.Errorf("normalized BL = %#x, want opcode-only %#x", got, uint32(0b100101)<<26)
	}
}
