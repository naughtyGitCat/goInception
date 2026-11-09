package main

import (
	"net"
	"testing"
)

func TestNumericSessionReadback(t *testing.T) {
	values := []string{"1", "7200", "7200000000", "7200000000", "3221701242"}
	var payload []byte
	for _, v := range values {
		payload = append(payload, byte(len(v)))
		payload = append(payload, []byte(v)...)
	}
	got := numericCells(payload)
	if len(got) != 5 || got[2] != "7200000000" || got[4] != "3221701242" {
		t.Fatalf("unexpected numeric-only session proof: %v", got)
	}
	for _, bad := range [][]byte{{255, 1, 2}, {3, 'a', 'b', 'c'}, {10, '1'}, {1, '1', 1, '2'}} {
		if numericCells(bad) != nil {
			t.Fatalf("non-state packet retained: %v", bad)
		}
	}
}
func TestPacketLengthAndSequence(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() { _, _ = a.Write([]byte{3, 0, 0, 7, 3, 'O', 'K'}) }()
	h, p, err := packet(b)
	if err != nil || h[3] != 7 || string(p) != "\x03OK" {
		t.Fatalf("packet framing error %v %v %v", h, p, err)
	}
}
