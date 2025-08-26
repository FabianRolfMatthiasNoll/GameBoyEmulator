package bus

import "testing"

// helper: tick bus n cycles
func tick(b *Bus, n int) { b.Tick(n) }

func TestPPU_STAT_HBlankInterrupt(t *testing.T) {
	b := New(make([]byte, 0x8000))
	// Turn LCD on
	b.Write(0xFF40, 0x80)
	// Enable STAT HBlank interrupt (bit3)
	b.Write(0xFF41, 1<<3)
	// Clear IF
	b.Write(0xFF0F, 0)
	// Start of frame: mode2 for 80 dots, then mode3 for 172, then mode0
	// Tick to just before HBlank transition, then next dot triggers mode0 and STAT IF
	tick(b, 80+172) // now should be at start of HBlank (mode 0)
	if (b.Read(0xFF0F) & (1 << 1)) == 0 {
		t.Fatalf("expected STAT IF on HBlank mode change")
	}
}

func TestPPU_LYC_InterruptAndFlag(t *testing.T) {
	b := New(make([]byte, 0x8000))
	// Turn LCD on
	b.Write(0xFF40, 0x80)
	// Enable LYC=LY STAT interrupt (bit6)
	b.Write(0xFF41, 1<<6)
	// Set LYC to 1
	b.Write(0xFF45, 0x01)
	// Clear IF
	b.Write(0xFF0F, 0)
	// Tick one full line to reach LY=1
	tick(b, 456)
	// STAT IF should be requested and coincidence flag set
	if (b.Read(0xFF0F) & (1 << 1)) == 0 {
		t.Fatalf("expected STAT IF on LYC=LY match at LY=1")
	}
	stat := b.Read(0xFF41)
	if (stat & (1 << 2)) == 0 {
		t.Fatalf("expected STAT coincidence flag set when LY==LYC")
	}
}

func TestPPU_VRAM_OAM_AccessRestrictions(t *testing.T) {
	b := New(make([]byte, 0x8000))
	// Turn LCD on
	b.Write(0xFF40, 0x80)
	// Move to HBlank (mode 0) to allow both VRAM and OAM writes
	tick(b, 80+172) // mode 0
	b.Write(0x8000, 0x11)
	b.Write(0xFE00, 0x22)
	// Advance to next line start (mode 2) then into mode 3
	tick(b, 456-252) // new line start (mode 2)
	tick(b, 80)      // enter mode 3
	// Attempt to overwrite values
	b.Write(0x8000, 0xAA)
	b.Write(0xFE00, 0xBB) // OAM also blocked in mode 3
	// Reads should return 0xFF while in blocked modes
	if got := b.Read(0x8000); got != 0xFF {
		t.Fatalf("VRAM read during mode3 got %02X want FF", got)
	}
	if got := b.Read(0xFE00); got != 0xFF {
		t.Fatalf("OAM read during mode3 got %02X want FF", got)
	}
	// Move to HBlank (mode 0)
	tick(b, 172)
	// Now reads should be allowed and original values should remain (writes were ignored)
	if got := b.Read(0x8000); got != 0x11 {
		t.Fatalf("VRAM value changed despite blocked write: got %02X want 11", got)
	}
	if got := b.Read(0xFE00); got != 0x22 {
		t.Fatalf("OAM value changed despite blocked write: got %02X want 22", got)
	}
}

func TestBus_OAMDMA_StepwiseAndBlocking(t *testing.T) {
	b := New(make([]byte, 0x8000))
	// Prepare source in WRAM at 0xC000.. for 160 bytes
	for i := 0; i < 0xA0; i++ {
		b.Write(0xC000+uint16(i), byte(i))
	}
	// Start DMA from 0xC000
	b.Write(0xFF46, 0xC0)
	// During DMA, OAM reads should be blocked and return 0xFF; writes ignored
	if got := b.Read(0xFE00); got != 0xFF {
		t.Fatalf("OAM read during DMA got %02X want FF", got)
	}
	b.Write(0xFE00, 0xEE) // should be ignored
	// After 80 cycles, still in progress
	tick(b, 80)
	if got := b.Read(0xFE10); got != 0xFF {
		t.Fatalf("mid-DMA OAM read got %02X want FF", got)
	}
	// Complete transfer (remaining 80 cycles)
	tick(b, 80)
	// Now OAM should contain the copied bytes
	for i := 0; i < 0xA0; i++ {
		if got := b.Read(0xFE00 + uint16(i)); got != byte(i) {
			t.Fatalf("OAM[%02X] got %02X want %02X", i, got, byte(i))
		}
	}
	// Now writes should be allowed again
	b.Write(0xFE00, 0x99)
	if got := b.Read(0xFE00); got != 0x99 {
		t.Fatalf("OAM write post-DMA failed: got %02X", got)
	}
}

func TestPPU_ModeSequenceVisibleLine(t *testing.T) {
	b := New(make([]byte, 0x8000))
	b.Write(0xFF40, 0x80) // LCD on
	// At start, LY=0, dot=0 -> mode 2
	if mode := b.Read(0xFF41) & 0x03; mode != 2 {
		t.Fatalf("mode at start got %d want 2", mode)
	}
	// After 80 dots -> enter mode 3
	tick(b, 80)
	if mode := b.Read(0xFF41) & 0x03; mode != 3 {
		t.Fatalf("mode at dot80 got %d want 3", mode)
	}
	// After 172 more -> enter mode 0
	tick(b, 172)
	if mode := b.Read(0xFF41) & 0x03; mode != 0 {
		t.Fatalf("mode at dot252 got %d want 0", mode)
	}
	// Finish line to next line start -> mode 2 and LY=1
	tick(b, 456-252)
	if ly := b.Read(0xFF44); ly != 1 {
		t.Fatalf("LY after 1 line got %d want 1", ly)
	}
	if mode := b.Read(0xFF41) & 0x03; mode != 2 {
		t.Fatalf("mode at new line got %d want 2", mode)
	}
}

func TestPPU_VBlankDurationAndIF(t *testing.T) {
	b := New(make([]byte, 0x8000))
	b.Write(0xFF40, 0x80) // LCD on
	b.Write(0xFF0F, 0)
	// Run 144 lines
	tick(b, 144*456)
	if ly := b.Read(0xFF44); ly != 144 {
		t.Fatalf("LY at vblank start got %d want 144", ly)
	}
	if mode := b.Read(0xFF41) & 0x03; mode != 1 {
		t.Fatalf("mode at vblank start got %d want 1", mode)
	}
	// VBlank IF must be set
	if (b.Read(0xFF0F) & 0x01) == 0 {
		t.Fatalf("VBlank IF not set on entering vblank")
	}
	// VBlank lasts 10 lines (144..153), then wraps to 0
	tick(b, 10*456)
	if ly := b.Read(0xFF44); ly != 0 {
		t.Fatalf("LY after vblank wrap got %d want 0", ly)
	}
}

func TestPPU_WriteLYResetsLineAndMode(t *testing.T) {
	b := New(make([]byte, 0x8000))
	b.Write(0xFF40, 0x80) // LCD on
	// Move to mid-line HBlank
	tick(b, 252)
	if mode := b.Read(0xFF41) & 0x03; mode != 0 {
		t.Fatalf("pre-reset mode got %d want 0", mode)
	}
	b.Write(0xFF44, 0x99) // any value resets LY and dot
	if ly := b.Read(0xFF44); ly != 0 {
		t.Fatalf("LY not reset to 0: %d", ly)
	}
	if mode := b.Read(0xFF41) & 0x03; mode != 2 {
		t.Fatalf("mode after LY reset got %d want 2", mode)
	}
}

func TestPPU_STAT_VBlankInterruptEnable(t *testing.T) {
	b := New(make([]byte, 0x8000))
	b.Write(0xFF40, 0x80) // LCD on
	b.Write(0xFF0F, 0)
	// Disable STAT VBlank interrupt
	b.Write(0xFF41, 0)
	tick(b, 144*456)
	// VBlank IF should be set, STAT IF should not
	if (b.Read(0xFF0F) & 0x01) == 0 {
		t.Fatalf("VBlank IF not set")
	}
	if (b.Read(0xFF0F) & 0x02) != 0 {
		t.Fatalf("STAT IF set unexpectedly when disabled")
	}
	// Clear IF and enable STAT VBlank (bit4)
	b.Write(0xFF0F, 0)
	b.Write(0xFF41, 1<<4)
	// Run another full frame to next vblank
	tick(b, 154*456)
	if (b.Read(0xFF0F) & 0x02) == 0 {
		t.Fatalf("STAT IF not set on VBlank when enabled")
	}
}
