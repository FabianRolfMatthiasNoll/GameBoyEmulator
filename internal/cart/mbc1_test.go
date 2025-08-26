package cart

import "testing"

func TestMBC1_ROMBanking(t *testing.T) {
	// Build a 128KB ROM with distinct bytes per bank at start of each bank
	rom := make([]byte, 128*1024)
	for bank := 0; bank < 8; bank++ {
		rom[bank*0x4000] = byte(bank)
	}
	m := NewMBC1(rom, 0)

	// Bank0 region reads from bank 0 in mode 0
	if got := m.Read(0x0000); got != 0x00 {
		t.Fatalf("bank0 read got %02X want 00", got)
	}

	// Switchable bank defaults to 1
	if got := m.Read(0x4000); got != 0x01 {
		t.Fatalf("bank1 read got %02X want 01", got)
	}

	// Select bank 3
	m.Write(0x2000, 0x03)
	if got := m.Read(0x4000); got != 0x03 {
		t.Fatalf("bank3 read got %02X want 03", got)
	}

	// Writing 0 maps to 1
	m.Write(0x2000, 0x00)
	if got := m.Read(0x4000); got != 0x01 {
		t.Fatalf("bank0->1 remap failed: got %02X", got)
	}
}

func TestMBC1_RAMBanking_Mode1(t *testing.T) {
	rom := make([]byte, 128*1024)
	m := NewMBC1(rom, 32*1024)

	// Enable RAM
	m.Write(0x0000, 0x0A)

	// Select mode 1 (RAM banking)
	m.Write(0x6000, 0x01)
	// Select RAM bank 2 via high bits
	m.Write(0x4000, 0x02)

	// Write/read in A000-BFFF should go to bank 2
	m.Write(0xA000, 0x77)
	if got := m.Read(0xA000); got != 0x77 {
		t.Fatalf("RAM bank2 RW failed: got %02X", got)
	}
}
