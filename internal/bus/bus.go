package bus

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"io"
	"os"

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
	joypLower4 byte // last computed lower 4 bits (active-low) for interrupt edge detection

	div  byte // FF04 (upper 8 bits of internal divider)
	tima byte // FF05
	tma  byte // FF06
	tac  byte // FF07 (lower 3 bits used)

	// Timer overflow handling: when TIMA overflows, it goes to 00 then reloads from TMA after a short delay
	// during which writes to TIMA cancel the reload.
	timaReloadDelay int // cycles remaining until reload from TMA; 0 means no pending reload

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

	// Boot ROM support
	bootROM     []byte
	bootEnabled bool

	// debug
	debugTimer bool
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
	if os.Getenv("GB_DEBUG_TIMER") != "" {
		b.debugTimer = true
	}
	return b
}

// PPU returns the internal PPU for read-only rendering helpers. Avoids breaking encapsulation for CPU access.
func (b *Bus) PPU() *ppu.PPU { return b.ppu }

// Cart returns the underlying cartridge for optional battery operations (read-only interface exposure).
func (b *Bus) Cart() cart.Cartridge { return b.cart }

func (b *Bus) Read(addr uint16) byte {
	switch {
	// Cartridge ROM and External RAM (banked) are handled by the cartridge
	case addr < 0x8000:
		// When boot ROM is enabled, it overlays 0x0000-0x00FF
		if b.bootEnabled && addr < 0x0100 && len(b.bootROM) >= 0x100 {
			return b.bootROM[addr]
		}
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
	// Boot ROM disable register (read returns 0xFF on DMG; keep simple)
	case addr == 0xFF50:
		return 0xFF
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
		b.updateJoypadIRQ()
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
			if b.debugTimer {
				fmt.Printf("[TMR] DIV write -> reset (div=0000) tima=%02X tma=%02X tac=%02X reload=%d\n", b.tima, b.tma, b.tac, b.timaReloadDelay)
			}
		return
	case addr == 0xFF05:
		// Writing TIMA during a pending reload cancels the reload and sets TIMA to the written value.
		b.tima = value
		if b.timaReloadDelay > 0 {
			b.timaReloadDelay = 0
		}
			if b.debugTimer {
				fmt.Printf("[TMR] TIMA write %02X tma=%02X tac=%02X reload=%d\n", value, b.tma, b.tac, b.timaReloadDelay)
			}
		return
	case addr == 0xFF06:
		b.tma = value
			if b.debugTimer {
				fmt.Printf("[TMR] TMA write %02X (tima=%02X tac=%02X reload=%d)\n", value, b.tima, b.tac, b.timaReloadDelay)
			}
		return
	case addr == 0xFF07:
		// Changing TAC can cause a falling edge on the timer input; handle increment accordingly.
		oldInput := b.timerInput()
		b.tac = value & 0x07
		if oldInput && !b.timerInput() {
			b.incrementTIMA()
		}
			if b.debugTimer {
				fmt.Printf("[TMR] TAC write %02X (input %v->%v) tima=%02X tma=%02X reload=%d\n", b.tac, oldInput, b.timerInput(), b.tima, b.tma, b.timaReloadDelay)
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
	case addr == 0xFF50:
		// Any non-zero write disables the boot ROM overlay
		if value != 0x00 {
			b.bootEnabled = false
		}
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
	b.updateJoypadIRQ()
}

// SetSerialWriter sets a sink that receives bytes written via the serial port.
func (b *Bus) SetSerialWriter(w io.Writer) { b.sw = w }

// SetBootROM loads a DMG boot ROM to be mapped at 0x0000-0x00FF until disabled via 0xFF50 write.
func (b *Bus) SetBootROM(data []byte) {
	b.bootROM = nil
	b.bootEnabled = false
	if len(data) >= 0x100 {
		b.bootROM = make([]byte, 0x100)
		copy(b.bootROM, data[:0x100])
		b.bootEnabled = true
	}
}

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
		falling := oldInput && !newInput

		// First, handle delayed TIMA reload if pending; on expiry, reload then allow an increment in this cycle
		if b.timaReloadDelay > 0 {
			b.timaReloadDelay--
			if b.timaReloadDelay == 0 {
				// On expiry, load TMA and request interrupt before processing any increment for this cycle
				b.tima = b.tma
				b.ifReg |= 1 << 2
			}
		}

		// Apply falling-edge increment after potential reload so edge on reload cycle increments reloaded value
		if falling {
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
	// During a pending reload delay, further increments are ignored (until reload or cancellation)
	if b.timaReloadDelay > 0 {
		return
	}
	if b.tima == 0xFF {
		// Overflow: set to 0x00 now, schedule delayed reload from TMA and IF request
		b.tima = 0x00
	// Reload occurs 4 cycles after the overflow, handled in Tick before edge increments
	b.timaReloadDelay = 4
		return
	}
	b.tima++
}

// PPU step: very simplified mode scheduling and LY counter
// PPU-specific helpers moved to internal/ppu

// updateJoypadIRQ recomputes JOYP lower 4 bits (active-low) and raises IF bit 4 on any 1->0 transition.
func (b *Bus) updateJoypadIRQ() {
	newLower := byte(0x0F)
	// P14 low selects D-Pad
	if (b.joypSelect & 0x10) == 0 {
		if b.joypad&JoypRight != 0 {
			newLower &^= 0x01
		}
		if b.joypad&JoypLeft != 0 {
			newLower &^= 0x02
		}
		if b.joypad&JoypUp != 0 {
			newLower &^= 0x04
		}
		if b.joypad&JoypDown != 0 {
			newLower &^= 0x08
		}
	}
	// P15 low selects Buttons
	if (b.joypSelect & 0x20) == 0 {
		if b.joypad&JoypA != 0 {
			newLower &^= 0x01
		}
		if b.joypad&JoypB != 0 {
			newLower &^= 0x02
		}
		if b.joypad&JoypSelectBtn != 0 {
			newLower &^= 0x04
		}
		if b.joypad&JoypStart != 0 {
			newLower &^= 0x08
		}
	}
	// Edge: previously 1, now 0 -> trigger IF bit 4
	falling := b.joypLower4 &^ newLower
	if falling != 0 {
		b.ifReg |= 1 << 4
	}
	b.joypLower4 = newLower
}

// --- Save/Load state ---
type busState struct {
	WRAM      [0x2000]byte
	HRAM      [0x7F]byte
	IE, IF    byte
	JoypSel   byte
	Joypad    byte
	JoypL4    byte
	DIV       byte
	TIMA      byte
	TMA       byte
	TAC       byte
	TIMARelay int
	SB, SC    byte
	DivInt    uint16
	DMA       byte
	DMAActive bool
	DMASrc    uint16
	DMAIdx    int
	BootEn    bool
	// PPU and cartridge will handle their own state via their interfaces
}

func (b *Bus) SaveState() []byte {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	s := busState{
		WRAM: b.wram, HRAM: b.hram,
		IE: b.ie, IF: b.ifReg,
		JoypSel: b.joypSelect, Joypad: b.joypad, JoypL4: b.joypLower4,
		DIV: b.div, TIMA: b.tima, TMA: b.tma, TAC: b.tac, TIMARelay: b.timaReloadDelay,
		SB: b.sb, SC: b.sc, DivInt: b.divInternal,
	DMA: b.dma, DMAActive: b.dmaActive, DMASrc: b.dmaSrc, DMAIdx: b.dmaIndex,
	BootEn: b.bootEnabled,
	}
	_ = enc.Encode(s)
	// Append PPU and Cart states after a simple header so we can restore later
	// PPU state
	if b.ppu != nil {
		ps := b.ppu.SaveState()
		_ = enc.Encode(ps)
	} else {
		_ = enc.Encode([]byte(nil))
	}
	// Cart state
	if bb, ok := b.cart.(interface{ SaveState() []byte }); ok {
		cs := bb.SaveState()
		_ = enc.Encode(cs)
	} else {
		_ = enc.Encode([]byte(nil))
	}
	return buf.Bytes()
}

func (b *Bus) LoadState(data []byte) {
	dec := gob.NewDecoder(bytes.NewReader(data))
	var s busState
	if err := dec.Decode(&s); err != nil { return }
	b.wram = s.WRAM
	b.hram = s.HRAM
	b.ie, b.ifReg = s.IE, s.IF
	b.joypSelect, b.joypad, b.joypLower4 = s.JoypSel, s.Joypad, s.JoypL4
	b.div, b.tima, b.tma, b.tac, b.timaReloadDelay = s.DIV, s.TIMA, s.TMA, s.TAC, s.TIMARelay
	b.sb, b.sc, b.divInternal = s.SB, s.SC, s.DivInt
	b.dma, b.dmaActive, b.dmaSrc, b.dmaIndex = s.DMA, s.DMAActive, s.DMASrc, s.DMAIdx
	b.bootEnabled = s.BootEn
	// PPU
	var ps []byte
	if err := dec.Decode(&ps); err == nil && b.ppu != nil {
		b.ppu.LoadState(ps)
	}
	// Cart
	var cs []byte
	if err := dec.Decode(&cs); err == nil {
		if bb, ok := b.cart.(interface{ LoadState([]byte) }); ok {
			bb.LoadState(cs)
		}
	}
}
