package bus

type Bus struct {
	rom []byte
	ram [0x2000]byte // 8KB internal RAM
}

func New(rom []byte) *Bus {
	return &Bus{
		rom: rom,
	}
}

func (b *Bus) Read(addr uint16) byte {
	switch {
	case addr < 0x8000: // ROM area
		if int(addr) < len(b.rom) {
			return b.rom[addr]
		}
		return 0xFF // out-of-bounds read
	case addr >= 0xC000 && addr < 0xDFFF: // Internal RAM
		return b.ram[addr-0xC000]
	default:
		return 0xFF // unmapped

	}
}

func (b *Bus) Write(addr uint16, value byte) {
	switch {
	case addr >= 0xC000 && addr < 0xDFFF: // Internal RAM
		b.ram[addr-0xC000] = value
	}
}
