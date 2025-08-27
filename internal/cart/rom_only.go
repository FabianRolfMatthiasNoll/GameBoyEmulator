package cart

// ROMOnly implements a simple cartridge without MBC or external RAM.
type ROMOnly struct {
	rom []byte
}

func NewROMOnly(rom []byte) *ROMOnly {
	return &ROMOnly{rom: rom}
}

func (c *ROMOnly) Read(addr uint16) byte {
	switch {
	case addr < 0x8000: // ROM fixed area
		if int(addr) < len(c.rom) {
			return c.rom[addr]
		}
		return 0xFF
	case addr >= 0xA000 && addr <= 0xBFFF: // no external RAM
		return 0xFF
	default:
		return 0xFF
	}
}

func (c *ROMOnly) Write(addr uint16, value byte) {
	// ROM-only: writes are ignored (including 0x0000–0x7FFF and 0xA000–0xBFFF)
}

func (c *ROMOnly) SaveState() []byte { return nil }
func (c *ROMOnly) LoadState(data []byte) {}
