package bus

import "testing"

func TestBus_ROMAndRAM(t *testing.T) {
	rom := make([]byte, 0x8000)
	rom[0x0100] = 0x42
	b := New(rom)

	if got := b.Read(0x0100); got != 0x42 {
		t.Fatalf("ROM read got %02x, want 42", got)
	}

	// RAM write+read
	b.Write(0xC000, 0x99)
	if got := b.Read(0xC000); got != 0x99 {
		t.Fatalf("RAM read got %02x, want 99", got)
	}

	// Unmapped should return 0xFF
	if got := b.Read(0xE000); got != 0xFF {
		t.Fatalf("Unmapped got %02x, want FF", got)
	}
}
