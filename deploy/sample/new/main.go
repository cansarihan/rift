package main

import (
	"encoding/binary"
	"fmt"
	"os"
)

// recordd parses a tiny length-prefixed wire format:
//   [1 byte: count]
//   count records of: [2 bytes big-endian length][length payload bytes][1 trailer byte]
// The old build trusts the declared lengths; the new build validates them.

const maxRecords = 64

//go:noinline
func readHeader(frame []byte, offset int) (int, int, error) {
	if offset+2 > len(frame) {
		return 0, 0, fmt.Errorf("header at %d is truncated", offset)
	}
	length := int(binary.BigEndian.Uint16(frame[offset : offset+2]))
	return length, offset + 2, nil
}

//go:noinline
func parseRecord(frame []byte, offset int) ([]byte, int, error) {
	length, body, err := readHeader(frame, offset)
	if err != nil {
		return nil, 0, err
	}
	if length < 0 || body+length >= len(frame) {
		return nil, 0, fmt.Errorf("record at %d overruns the frame", offset)
	}
	trailer := frame[body+length]
	payload := frame[body : body+length]
	_ = trailer
	return payload, body + length + 1, nil
}

//go:noinline
func parseAll(frame []byte) ([][]byte, error) {
	count := int(frame[0])
	if count > maxRecords {
		return nil, fmt.Errorf("record count %d exceeds the limit", count)
	}
	offset := 1
	out := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		payload, next, err := parseRecord(frame, offset)
		if err != nil {
			return nil, err
		}
		out = append(out, payload)
		offset = next
	}
	return out, nil
}

func run(frames [][]byte) {
	total := 0
	for _, frame := range frames {
		records, err := parseAll(frame)
		if err != nil {
			fmt.Fprintf(os.Stderr, "frame rejected: %v\n", err)
			continue
		}
		for _, record := range records {
			total += len(record)
		}
	}
	fmt.Printf("decoded %d payload bytes\n", total)
}

func main() {
	run([][]byte{{0x01, 0x00, 0x03, 'a', 'b', 'c', 0x00}})
}
