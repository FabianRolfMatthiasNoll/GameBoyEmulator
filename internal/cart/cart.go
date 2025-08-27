package cart

// Cartridge defines the minimal interface the Bus needs for ROM/RAM banking.
// Implementations can be ROM-only or MBC variants. Addresses are CPU addresses.
type Cartridge interface {
	// Read returns a byte for ROM (0x0000–0x7FFF) and external RAM (0xA000–0xBFFF).
	Read(addr uint16) byte
	// Write handles MBC control writes (0x0000–0x7FFF) and external RAM writes (0xA000–0xBFFF).
	Write(addr uint16, value byte)
	// SaveState/LoadState serialize internal banking registers and external RAM for save states.
	SaveState() []byte
	LoadState(data []byte)
}

// BatteryBacked is an optional interface for cartridges with external RAM to be persisted.
// Implementations should return a copy of RAM bytes (may be empty if no RAM), and accept data to load.
type BatteryBacked interface {
	SaveRAM() []byte
	LoadRAM(data []byte)
}

// NewCartridge picks an implementation based on the ROM header.
func NewCartridge(rom []byte) Cartridge {
	h, err := ParseHeader(rom)
	if err != nil {
		return NewROMOnly(rom)
	}
	switch h.CartType {
	case 0x00:
		return NewROMOnly(rom)
	case 0x01, 0x02, 0x03: // MBC1 variants (RAM, RAM+BAT are transparent here)
		return NewMBC1(rom, h.RAMSizeBytes)
	case 0x0F, 0x10, 0x11, 0x12, 0x13: // MBC3 variants (RTC not implemented here)
		return NewMBC3(rom, h.RAMSizeBytes)
	case 0x19, 0x1A, 0x1B, 0x1C, 0x1D, 0x1E: // MBC5 variants
		return NewMBC5(rom, h.RAMSizeBytes)
	default:
		// Fallback to ROM-only for unknown types to allow some homebrew/tests to run
		return NewROMOnly(rom)
	}
}
