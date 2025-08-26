package bus

import (
	"io"

	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/cart"
	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/ppu"
)

// Bus wires CPU-visible address space to cartridge, WRAM, HRAM, and IO.
// This is an early skeleton: IO, OAM, VRAM etc. are stubbed as 0xFF.
type Bus struct {
	cart cart.Cartridge

	// Work RAM (WRAM) 8 KiB at 0xC000–0xDFFF; Echo 0xE000–0xFDFF mirrors C000–DDFF.
	wram [0x2000]byte

	// High RAM (HRAM) 0xFF80–0xFFFE (127 bytes)
	hram [0x7F]byte

	// PPU encapsulates VRAM/OAM and LCDC/STAT timing
	ppu *ppu.PPU

	// Interrupt registers
	ie    byte // IE at 0xFFFF
	ifReg byte // IF at 0xFF0F (lower 5 bits used)

	// JOYP and Timers (scaffold only; ticking not implemented yet)
	joypSelect byte // bits 5-4 as last written
	joypad     byte // bitmask of pressed buttons (1=pressed), see constants below

	div  byte // FF04 (upper 8 bits of internal divider)
	tima byte // FF05
	tma  byte // FF06
	tac  byte // FF07 (lower 3 bits used)

	// Serial
	sb byte      // FF01 data
	sc byte      // FF02 control (bit7 start, bit0 clock source; we do immediate external)
	sw io.Writer // sink for serial output (optional)

	// Internal 16-bit divider that increments every T-cycle; DIV reads upper 8 bits
	divInternal uint16

	// DMA register (still handled here for copy trigger)
	dma byte // FF46

	// OAM DMA state
	dmaActive bool
	dmaSrc    uint16
	dmaIndex  int
}

// New constructs a Bus with a ROM-only cartridge for convenience.
func New(rom []byte) *Bus {
	return NewWithCartridge(cart.NewCartridge(rom))
}

// NewWithCartridge wires a provided cartridge implementation.
func NewWithCartridge(c cart.Cartridge) *Bus {
	b := &Bus{cart: c}
	// hook PPU to request IF bits through bus
	b.ppu = ppu.New(func(bit int) { b.ifReg |= 1 << bit })
	return b
}

func (b *Bus) Read(addr uint16) byte {
	switch {
	// Cartridge ROM and External RAM (banked) are handled by the cartridge
	case addr < 0x8000:
		return b.cart.Read(addr)
	// VRAM (via PPU)
	case addr >= 0x8000 && addr <= 0x9FFF:
		return b.ppu.CPURead(addr)
	case addr >= 0xA000 && addr <= 0xBFFF:
		return b.cart.Read(addr)

	// Work RAM 0xC000–0xDFFF (8 KiB); note upper bound is inclusive 0xDFFF
	case addr >= 0xC000 && addr <= 0xDFFF:
		return b.wram[addr-0xC000]

	// Echo RAM 0xE000–0xFDFF mirrors 0xC000–0xDDFF
	case addr >= 0xE000 && addr <= 0xFDFF:
		mirror := addr - 0x2000
		return b.wram[mirror-0xC000]

	// High RAM 0xFF80–0xFFFE (IE at 0xFFFF not covered yet)
	case addr >= 0xFF80 && addr <= 0xFFFE:
		return b.hram[addr-0xFF80]
	// OAM via PPU (reads blocked during DMA)
	case addr >= 0xFE00 && addr <= 0xFE9F:
		if b.dmaActive {
			return 0xFF
		}
		return b.ppu.CPURead(addr)
	// IO: JOYP at 0xFF00
	case addr == 0xFF00:
		// Upper bits 7-6 read as 1, bits 5-4 reflect selection, bits 3-0 depend on selected group(s)
		res := byte(0xC0 | (b.joypSelect & 0x30) | 0x0F)
		// If P14 (bit4) == 0, select D-Pad (Right, Left, Up, Down => bits 0..3)
		if (b.joypSelect & 0x10) == 0 {
			// Clear bits for pressed D-Pad buttons (active-low)
			if b.joypad&JoypRight != 0 {
				res &^= 0x01
			}
			if b.joypad&JoypLeft != 0 {
				res &^= 0x02
			}
			if b.joypad&JoypUp != 0 {
				res &^= 0x04
			}
			if b.joypad&JoypDown != 0 {
				res &^= 0x08
			}
		}
		// If P15 (bit5) == 0, select Buttons (A, B, Select, Start => bits 0..3)
		if (b.joypSelect & 0x20) == 0 {
			if b.joypad&JoypA != 0 {
				res &^= 0x01
			}
			if b.joypad&JoypB != 0 {
				res &^= 0x02
			}
			if b.joypad&JoypSelectBtn != 0 {
				res &^= 0x04
			}
			if b.joypad&JoypStart != 0 {
				res &^= 0x08
			}
		}
		return res
	// IO: Timers
	case addr == 0xFF04:
		return b.div
	case addr == 0xFF05:
		return b.tima
	case addr == 0xFF06:
		return b.tma
	case addr == 0xFF07:
		return 0xF8 | (b.tac & 0x07)
	// Serial
	case addr == 0xFF01:
		return b.sb
	case addr == 0xFF02:
		// upper bits read as 1 except bit7 reflects transfer in progress; we complete immediately
		return 0x7E | (b.sc & 0x81)
	// LCDC/STAT/LY/LYC and scroll/window via PPU
	case addr == 0xFF40, addr == 0xFF41, addr == 0xFF42, addr == 0xFF43,
		addr == 0xFF44, addr == 0xFF45,
		addr == 0xFF47, addr == 0xFF48, addr == 0xFF49,
		addr == 0xFF4A, addr == 0xFF4B:
		return b.ppu.CPURead(addr)
	case addr == 0xFF46:
		return b.dma
	// IO: IF at 0xFF0F, other IO not implemented (return 0xFF)
	case addr == 0xFF0F:
		return 0xE0 | (b.ifReg & 0x1F)
	// IE at 0xFFFF
	case addr == 0xFFFF:
		return b.ie
	}
	// TODO: Add VRAM, OAM, IO registers, IE
	return 0xFF
}

func (b *Bus) Write(addr uint16, value byte) {
	switch {
	// Cartridge control and external RAM writes
	case addr < 0x8000:
		b.cart.Write(addr, value)
		return
	// VRAM via PPU
	case addr >= 0x8000 && addr <= 0x9FFF:
		b.ppu.CPUWrite(addr, value)
		return
	case addr >= 0xA000 && addr <= 0xBFFF:
		b.cart.Write(addr, value)
		return

	// Work RAM
	case addr >= 0xC000 && addr <= 0xDFFF:
		b.wram[addr-0xC000] = value
		return

	// Echo RAM mirrors C000–DDFF
	case addr >= 0xE000 && addr <= 0xFDFF:
		mirror := addr - 0x2000
		if mirror >= 0xC000 && mirror <= 0xDDFF {
			b.wram[mirror-0xC000] = value
		}
		return

	// High RAM
	case addr >= 0xFF80 && addr <= 0xFFFE:
		b.hram[addr-0xFF80] = value
		return
	// OAM via PPU (writes ignored during DMA)
	case addr >= 0xFE00 && addr <= 0xFE9F:
		if b.dmaActive {
			return
		}
		b.ppu.CPUWrite(addr, value)
		return
	// IO: JOYP at 0xFF00
	case addr == 0xFF00:
		b.joypSelect = value & 0x30
		return
	// IO: Timers
	case addr == 0xFF04:
		// Writing any value to DIV resets the internal divider and may cause a TIMA increment
		// if the timer input experiences a falling edge due to the reset.
		oldInput := b.timerInput()
		b.divInternal = 0
		b.div = 0
		if oldInput && !b.timerInput() {
			b.incrementTIMA()
		}
		return
	case addr == 0xFF05:
		b.tima = value
		return
	case addr == 0xFF06:
		b.tma = value
		return
	case addr == 0xFF07:
		// Changing TAC can cause a falling edge on the timer input; handle increment accordingly.
		oldInput := b.timerInput()
		b.tac = value & 0x07
		if oldInput && !b.timerInput() {
			b.incrementTIMA()
		}
		return
	// Serial
	case addr == 0xFF01:
		b.sb = value
		return
	case addr == 0xFF02:
		b.sc = value & 0x81
		if (b.sc & 0x80) != 0 {
			// Start transfer: we do immediate completion; write byte to sink if present
			if b.sw != nil {
				_, _ = b.sw.Write([]byte{b.sb})
			}
			// Request serial interrupt (IF bit 3)
			b.ifReg |= 1 << 3
			// Clear transfer start bit to indicate done
			b.sc &^= 0x80
		}
		return
	// LCDC/STAT/LY/LYC and scroll/window via PPU
	case addr == 0xFF40, addr == 0xFF41, addr == 0xFF42, addr == 0xFF43,
		addr == 0xFF44, addr == 0xFF45,
		addr == 0xFF47, addr == 0xFF48, addr == 0xFF49,
		addr == 0xFF4A, addr == 0xFF4B:
		b.ppu.CPUWrite(addr, value)
		return
	case addr == 0xFF46:
		// OAM DMA: initiate 160-byte transfer from value*0x100 to FE00, 1 byte per cycle
		b.dma = value
		b.dmaActive = true
		b.dmaSrc = uint16(value) << 8
		b.dmaIndex = 0
		return
	// IO: IF at 0xFF0F
	case addr == 0xFF0F:
		b.ifReg = value & 0x1F
		return
	// IE at 0xFFFF
	case addr == 0xFFFF:
		b.ie = value
		return
	}
	// Unhandled regions are ignored for now
}

// Joypad button bitmasks for SetJoypadState. Bits set mean "pressed".
const (
	JoypRight     = 1 << 0
	JoypLeft      = 1 << 1
	JoypUp        = 1 << 2
	JoypDown      = 1 << 3
	JoypA         = 1 << 4
	JoypB         = 1 << 5
	JoypSelectBtn = 1 << 6
	JoypStart     = 1 << 7
)

// SetJoypadState sets which buttons are currently pressed.
// Pass a mask using the Joyp* constants above; set bits mean pressed.
func (b *Bus) SetJoypadState(mask byte) {
	b.joypad = mask
}

// SetSerialWriter sets a sink that receives bytes written via the serial port.
func (b *Bus) SetSerialWriter(w io.Writer) { b.sw = w }

// Tick advances timers by the given number of CPU cycles.
// True-to-hardware: TIMA increments on falling edge of selected divider bit
// determined by TAC (00:bit9, 01:bit3, 10:bit5, 11:bit7), gated by TAC enable.
func (b *Bus) Tick(cycles int) {
	if cycles <= 0 {
		return
	}
	for i := 0; i < cycles; i++ {
		oldInput := b.timerInput()
		b.divInternal++
		b.div = byte(b.divInternal >> 8)
		newInput := b.timerInput()
		if oldInput && !newInput {
			b.incrementTIMA()
		}
		// Tick PPU via module
		if b.ppu != nil {
			b.ppu.Tick(1)
		}

		// Step OAM DMA (1 byte per cycle) if active
		if b.dmaActive {
			if b.dmaIndex < 0xA0 {
				v := b.Read(b.dmaSrc + uint16(b.dmaIndex))
				b.ppu.CPUWrite(0xFE00+uint16(b.dmaIndex), v)
				b.dmaIndex++
			}
			if b.dmaIndex >= 0xA0 {
				b.dmaActive = false
			}
		}
	}
}

// timerInput computes the current timer clock input (after TAC gating).
func (b *Bus) timerInput() bool {
	if (b.tac & 0x04) == 0 { // timer disabled
		return false
	}
	var bit uint
	switch b.tac & 0x03 {
	case 0x00:
		bit = 9 // 4096 Hz
	case 0x01:
		bit = 3 // 262144 Hz
	case 0x02:
		bit = 5 // 65536 Hz
	case 0x03:
		bit = 7 // 16384 Hz
	}
	return ((b.divInternal >> bit) & 1) != 0
}

func (b *Bus) incrementTIMA() {
	if b.tima == 0xFF {
		b.tima = b.tma
		b.ifReg |= 1 << 2 // request Timer interrupt
	} else {
		b.tima++
	}
}

// PPU step: very simplified mode scheduling and LY counter
// PPU-specific helpers moved to internal/ppu
