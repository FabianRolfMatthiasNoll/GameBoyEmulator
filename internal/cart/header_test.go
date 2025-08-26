package cart

import (
	"encoding/binary"
	"testing"
)

// buildROM makes a synthetic ROM with a valid header & checksums.
// size should match the ROM size code (e.g. 64*1024 for code 0x01).
func buildROM(title string, cartType, romSizeCode, ramSizeCode byte, size int) []byte {
	rom := make([]byte, size)

	// Nintendo logo (optional for emulator, useful for realism)
	copy(rom[0x0104:0x0104+len(nintendoLogo)], nintendoLogo[:])

	// Title 0x0134–0x0143 (16 bytes max)
	tbytes := []byte(title)
	if len(tbytes) > 16 {
		tbytes = tbytes[:16]
	}
	copy(rom[0x0134:0x0144], tbytes)

	// Header fields
	rom[0x0143] = 0x00             // CGB flag
	rom[0x0144], rom[0x0145] = '0', '1' // New licensee ("01")
	rom[0x0146] = 0x00             // SGB flag
	rom[0x0147] = cartType         // Cartridge type (e.g., 0x01 = MBC1)
	rom[0x0148] = romSizeCode      // ROM size code (e.g., 0x01 = 64 KiB)
	rom[0x0149] = ramSizeCode      // RAM size code (e.g., 0x02 = 8 KiB)
	rom[0x014A] = 0x00             // Destination
	rom[0x014B] = 0x33             // Old licensee (use new licensee)
	rom[0x014C] = 0x01             // Mask ROM version

	// Header checksum over 0x0134–0x014C (Pan Docs algorithm)
	var hsum byte
	for addr := 0x0134; addr <= 0x014C; addr++ {
		hsum = hsum - rom[addr] - 1
	}
	rom[0x014D] = hsum

	// Global checksum: sum of all bytes except 0x014E–0x014F (big-endian)
	var gsum uint16
	for i := 0; i < len(rom); i++ {
		if i == 0x014E || i == 0x014F {
			continue
		}
		gsum += uint16(rom[i])
	}
	binary.BigEndian.PutUint16(rom[0x014E:0x0150], gsum)

	return rom
}

func TestParseHeader_Basic(t *testing.T) {
	rom := buildROM("TEST", 0x01, 0x01, 0x02, 64*1024) // MBC1, 64KiB, 8KiB RAM

	h, err := ParseHeader(rom)
	if err != nil {
		t.Fatalf("ParseHeader error: %v", err)
	}
	if h.Title != "TEST" {
		t.Fatalf("Title got %q want %q", h.Title, "TEST")
	}
	if h.CartType != 0x01 || h.CartTypeStr != "MBC1 (variants)" {
		t.Fatalf("CartType got %#02x / %s", h.CartType, h.CartTypeStr)
	}
	if h.ROMSizeBytes != 64*1024 || h.ROMBanks != 4 {
		t.Fatalf("ROM size decode got %d bytes / %d banks", h.ROMSizeBytes, h.ROMBanks)
	}
	if h.RAMSizeBytes != 8*1024 {
		t.Fatalf("RAM size decode got %d", h.RAMSizeBytes)
	}
	if !HeaderChecksumOK(rom) {
		t.Fatalf("HeaderChecksumOK = false, want true")
	}

	// Recompute global checksum to cross-check the parsed value
	var gsum uint16
	for i := 0; i < len(rom); i++ {
		if i == 0x014E || i == 0x014F {
			continue
		}
		gsum += uint16(rom[i])
	}
	if h.GlobalChecksum != gsum {
		t.Fatalf("Global checksum got %#04x want %#04x", h.GlobalChecksum, gsum)
	}
}

func TestHeaderChecksum_Bad(t *testing.T) {
	rom := buildROM("TEST", 0x00, 0x00, 0x00, 32*1024)
	rom[0x0134] ^= 0xFF // corrupt a header byte
	if HeaderChecksumOK(rom) {
		t.Fatalf("HeaderChecksumOK = true, want false after corruption")
	}
}

func TestParseHeader_ShortROM(t *testing.T) {
	short := make([]byte, 0x140) // too small (header needs through 0x014F)
	if _, err := ParseHeader(short); err == nil {
		t.Fatalf("expected error on too-small ROM, got nil")
	}
}
