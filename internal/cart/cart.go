package cart

// Cartridge defines the minimal interface the Bus needs for ROM/RAM banking.
// Implementations can be ROM-only or MBC variants. Addresses are CPU addresses.
type Cartridge interface {
	// Read returns a byte for ROM (0x0000–0x7FFF) and external RAM (0xA000–0xBFFF).
	Read(addr uint16) byte
	// Write handles MBC control writes (0x0000–0x7FFF) and external RAM writes (0xA000–0xBFFF).
	Write(addr uint16, value byte)
}
