// Package callscan recovers the direct calls a function makes without a full
// disassembler. It decodes only the one instruction that matters for a patch
// diff — the direct call — on amd64 and arm64, and resolves each target back to
// a symbol. That is enough to notice when a build starts calling the runtime's
// bounds and overflow check helpers, which is the fingerprint of a silently
// added safety check.
package callscan

import (
	"encoding/binary"
	"sort"

	"github.com/cansarihan/rift/internal/binfile"
)

// Resolver maps a virtual address to the name of the function that contains it.
type Resolver interface {
	Resolve(addr uint64) (string, bool)
}

// Callees returns the sorted, de-duplicated set of functions that fn calls
// directly, as resolved against the image fn belongs to. Only direct calls are
// decoded; indirect and tail calls through registers are not reachable without
// data-flow analysis and are deliberately out of scope.
func Callees(arch binfile.Arch, fn binfile.Func, resolver Resolver) []string {
	var targets []uint64
	switch arch {
	case binfile.ArchAMD64:
		targets = amd64Calls(fn)
	case binfile.ArchARM64:
		targets = arm64Calls(fn)
	default:
		return nil
	}

	seen := make(map[string]struct{}, len(targets))
	for _, addr := range targets {
		if name, ok := resolver.Resolve(addr); ok && name != fn.Name {
			seen[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// amd64Calls finds every `CALL rel32` (opcode 0xE8) and computes its target.
// The scan is byte-wise, so a 0xE8 inside another instruction's operand can be
// misread; targets that do not land on a known function are dropped by the
// resolver, which keeps the noise out of the result.
func amd64Calls(fn binfile.Func) []uint64 {
	code := fn.Code
	var targets []uint64
	for i := 0; i+5 <= len(code); i++ {
		if code[i] != 0xe8 {
			continue
		}
		rel := int32(binary.LittleEndian.Uint32(code[i+1 : i+5]))
		next := fn.Addr + uint64(i) + 5
		targets = append(targets, next+uint64(int64(rel)))
	}
	return targets
}

// NormalizedCode returns a copy of fn's code with the operands that depend only
// on where the linker placed things zeroed out: relative call and branch
// displacements, and arm64 ADRP page immediates. Two functions with identical
// logic compile to identical normalized code even when a layout shift moved
// every callee, so a byte comparison of the normalized form no longer mistakes
// relocation for a real change. amd64 RIP-relative data loads are not decoded
// here and are left in place; the diff engine filters that residue separately.
func NormalizedCode(arch binfile.Arch, fn binfile.Func) []byte {
	out := make([]byte, len(fn.Code))
	copy(out, fn.Code)
	switch arch {
	case binfile.ArchAMD64:
		normalizeAMD64(out)
	case binfile.ArchARM64:
		normalizeARM64(out)
	}
	return out
}

func normalizeAMD64(code []byte) {
	for i := 0; i < len(code); {
		switch {
		case code[i] == 0xe8 || code[i] == 0xe9: // CALL/JMP rel32
			if i+5 <= len(code) {
				zero(code[i+1 : i+5])
			}
			i += 5
		case code[i] == 0x0f && i+1 < len(code) && code[i+1] >= 0x80 && code[i+1] <= 0x8f: // Jcc rel32
			if i+6 <= len(code) {
				zero(code[i+2 : i+6])
			}
			i += 6
		default:
			i++
		}
	}
}

func normalizeARM64(code []byte) {
	for i := 0; i+4 <= len(code); i += 4 {
		word := binary.LittleEndian.Uint32(code[i : i+4])
		switch {
		case word>>26 == 0b100101 || word>>26 == 0b000101: // BL / B imm26
			word &= 0xfc000000
		case word&0x9f000000 == 0x90000000: // ADRP: page-relative data address
			word &^= (0x3 << 29) | (0x7ffff << 5)
		default:
			continue
		}
		binary.LittleEndian.PutUint32(code[i:i+4], word)
	}
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// arm64Calls finds every `BL imm26` (top six bits 100101). Instructions are
// four-byte aligned, so the scan steps a word at a time and is far less prone to
// misreads than the amd64 one.
func arm64Calls(fn binfile.Func) []uint64 {
	code := fn.Code
	var targets []uint64
	for i := 0; i+4 <= len(code); i += 4 {
		word := binary.LittleEndian.Uint32(code[i : i+4])
		if word>>26 != 0b100101 {
			continue
		}
		imm := int64(word&0x03ffffff) << 2
		if imm&(1<<27) != 0 { // sign-extend the 28-bit signed offset
			imm |= ^int64(0) << 28
		}
		targets = append(targets, fn.Addr+uint64(i)+uint64(imm))
	}
	return targets
}
