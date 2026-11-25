// Package binfile opens an executable and recovers its functions from the
// symbol table. It understands ELF, Mach-O and PE, and exposes each function as
// a name, a virtual address and the slice of machine code that implements it, so
// a diff can compare two builds function by function without a disassembler.
package binfile

import (
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Arch is the instruction set a binary targets. Only the two that Rift decodes
// calls for are named explicitly; everything else is Unknown and still diffs at
// the byte level.
type Arch string

const (
	ArchAMD64   Arch = "amd64"
	ArchARM64   Arch = "arm64"
	ArchUnknown Arch = "unknown"
)

// Func is one function recovered from a binary: its symbol name, the virtual
// address it loads at, and the machine code between its start and the next
// symbol. Code is a view suitable for byte comparison and call scanning.
type Func struct {
	Name string
	Addr uint64
	Size uint64
	Code []byte
}

// Image is the set of functions recovered from one binary, kept sorted by
// address so a target address resolves to the function that contains it with a
// binary search.
type Image struct {
	Path   string
	Format string
	Arch   Arch
	Funcs  []Func

	byName map[string]int
}

// ErrNoSymbols is returned when a binary carries no usable function symbols, for
// example a fully stripped executable. A diff needs symbols to pair functions.
var ErrNoSymbols = errors.New("binary has no function symbols")

// Open reads a binary from disk and recovers its functions.
func Open(path string) (*Image, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-supplied binary to inspect
	if err != nil {
		return nil, err
	}
	if len(data) < 4 {
		return nil, fmt.Errorf("%s: not an executable", path)
	}

	var image *Image
	switch {
	case data[0] == 0x7f && data[1] == 'E' && data[2] == 'L' && data[3] == 'F':
		image, err = fromELF(data)
	case isMachO(data):
		image, err = fromMachO(data)
	case data[0] == 'M' && data[1] == 'Z':
		image, err = fromPE(data)
	default:
		return nil, fmt.Errorf("%s: unrecognized binary format", path)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	image.Path = path
	image.finish()
	if len(image.Funcs) == 0 {
		return nil, fmt.Errorf("%s: %w", path, ErrNoSymbols)
	}
	return image, nil
}

// New builds an Image from a known set of functions. It sorts them by address
// and indexes them by name, so the result supports Lookup and Resolve exactly
// as one returned by Open. It exists mainly so callers and tests can assemble an
// image without a file on disk.
func New(format string, arch Arch, funcs []Func) *Image {
	img := &Image{Format: format, Arch: arch, Funcs: append([]Func(nil), funcs...)}
	img.finish()
	return img
}

func isMachO(data []byte) bool {
	magics := []uint32{0xfeedface, 0xfeedfacf, 0xcefaedfe, 0xcffaedfe}
	got := uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24
	for _, m := range magics {
		if got == m {
			return true
		}
	}
	return false
}

// Lookup returns the function with the given name, if present.
func (img *Image) Lookup(name string) (Func, bool) {
	index, ok := img.byName[name]
	if !ok {
		return Func{}, false
	}
	return img.Funcs[index], true
}

// Resolve maps a virtual address to the name of the function that contains it.
// It is used to turn a decoded call target back into a symbol.
func (img *Image) Resolve(addr uint64) (string, bool) {
	funcs := img.Funcs
	index := sort.Search(len(funcs), func(i int) bool {
		return funcs[i].Addr > addr
	}) - 1
	if index < 0 {
		return "", false
	}
	fn := funcs[index]
	if addr >= fn.Addr && addr < fn.Addr+fn.Size {
		return fn.Name, true
	}
	return "", false
}

func (img *Image) finish() {
	sort.Slice(img.Funcs, func(i, j int) bool { return img.Funcs[i].Addr < img.Funcs[j].Addr })
	img.byName = make(map[string]int, len(img.Funcs))
	for i, fn := range img.Funcs {
		// A symbol table may list the same name twice (aliases); keep the first.
		if _, seen := img.byName[fn.Name]; !seen {
			img.byName[fn.Name] = i
		}
	}
}

// symbol is the format-independent shape we reduce each symbol table to before
// slicing code out of the text section.
type symbol struct {
	name string
	addr uint64
	size uint64
}

// build turns a text section (its virtual address and bytes) plus a list of
// function symbols into the sliced function list. When a symbol carries no
// explicit size, the gap to the next symbol is used.
func build(textAddr uint64, text []byte, syms []symbol) []Func {
	textEnd := textAddr + uint64(len(text))
	inText := syms[:0]
	for _, s := range syms {
		if s.addr >= textAddr && s.addr < textEnd {
			inText = append(inText, s)
		}
	}
	sort.Slice(inText, func(i, j int) bool { return inText[i].addr < inText[j].addr })

	funcs := make([]Func, 0, len(inText))
	for i, s := range inText {
		end := textEnd
		if i+1 < len(inText) {
			end = inText[i+1].addr
		}
		if s.size > 0 && s.addr+s.size < end {
			end = s.addr + s.size
		}
		if end <= s.addr {
			continue
		}
		start := s.addr - textAddr
		stop := end - textAddr
		if stop > uint64(len(text)) {
			stop = uint64(len(text))
		}
		code := make([]byte, stop-start)
		copy(code, text[start:stop])
		funcs = append(funcs, Func{Name: s.name, Addr: s.addr, Size: uint64(len(code)), Code: code})
	}
	return funcs
}

func fromELF(data []byte) (*Image, error) {
	file, err := elf.NewFile(readerAt(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	text := file.Section(".text")
	if text == nil {
		return nil, errors.New("no .text section")
	}
	body, err := text.Data()
	if err != nil {
		return nil, err
	}

	symbols, err := file.Symbols()
	if err != nil && !errors.Is(err, elf.ErrNoSymbols) {
		return nil, err
	}
	syms := make([]symbol, 0, len(symbols))
	for _, s := range symbols {
		if elf.ST_TYPE(s.Info) != elf.STT_FUNC || s.Value == 0 || s.Name == "" {
			continue
		}
		syms = append(syms, symbol{name: s.Name, addr: s.Value, size: s.Size})
	}

	arch := ArchUnknown
	switch file.Machine {
	case elf.EM_X86_64:
		arch = ArchAMD64
	case elf.EM_AARCH64:
		arch = ArchARM64
	}
	return &Image{Format: "elf", Arch: arch, Funcs: build(text.Addr, body, syms)}, nil
}

func fromMachO(data []byte) (*Image, error) {
	file, err := macho.NewFile(readerAt(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	text := file.Section("__text")
	if text == nil {
		return nil, errors.New("no __text section")
	}
	body, err := text.Data()
	if err != nil {
		return nil, err
	}

	var syms []symbol
	if file.Symtab != nil {
		textStart := text.Addr
		textEnd := text.Addr + text.Size
		for _, s := range file.Symtab.Syms {
			if s.Name == "" || s.Value < textStart || s.Value >= textEnd {
				continue
			}
			syms = append(syms, symbol{name: strings.TrimPrefix(s.Name, "_"), addr: s.Value})
		}
	}

	arch := ArchUnknown
	switch file.Cpu {
	case macho.CpuAmd64:
		arch = ArchAMD64
	case macho.CpuArm64:
		arch = ArchARM64
	}
	return &Image{Format: "macho", Arch: arch, Funcs: build(text.Addr, body, syms)}, nil
}

func fromPE(data []byte) (*Image, error) {
	file, err := pe.NewFile(readerAt(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	text := file.Section(".text")
	if text == nil {
		return nil, errors.New("no .text section")
	}
	body, err := text.Data()
	if err != nil {
		return nil, err
	}
	// PE section data can be padded; trim to the virtual size.
	if text.VirtualSize > 0 && uint32(len(body)) > text.VirtualSize {
		body = body[:text.VirtualSize]
	}

	base := imageBase(file)
	textVA := base + uint64(text.VirtualAddress)
	textEnd := textVA + uint64(len(body))

	var syms []symbol
	for _, s := range file.Symbols {
		if s.Name == "" || int(s.SectionNumber) <= 0 || int(s.SectionNumber) > len(file.Sections) {
			continue
		}
		section := file.Sections[s.SectionNumber-1]
		addr := base + uint64(section.VirtualAddress) + uint64(s.Value)
		if addr < textVA || addr >= textEnd {
			continue
		}
		syms = append(syms, symbol{name: s.Name, addr: addr})
	}

	arch := ArchUnknown
	switch file.Machine {
	case pe.IMAGE_FILE_MACHINE_AMD64:
		arch = ArchAMD64
	case pe.IMAGE_FILE_MACHINE_ARM64:
		arch = ArchARM64
	}
	return &Image{Format: "pe", Arch: arch, Funcs: build(textVA, body, syms)}, nil
}

func imageBase(file *pe.File) uint64 {
	switch header := file.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		return header.ImageBase
	case *pe.OptionalHeader32:
		return uint64(header.ImageBase)
	default:
		return 0
	}
}
