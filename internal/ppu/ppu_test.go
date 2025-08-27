package ppu

import (
	"testing"
)

// helper to read mode bits from STAT (FF41)
func statMode(p *PPU) byte { return p.CPURead(0xFF41) & 0x03 }

func TestPPUModeSequenceOneLine(t *testing.T) {
	var irqs []int
	p := New(func(bit int) { irqs = append(irqs, bit) })
	// Turn LCD on
	p.CPUWrite(0xFF40, 0x80)
	if m := statMode(p); m != 2 {
		t.Fatalf("expected mode 2 after LCD on, got %d", m)
	}
	// After 80 dots -> mode 3
	p.Tick(80)
	if m := statMode(p); m != 3 {
		t.Fatalf("expected mode 3 at dot 80, got %d", m)
	}
	// After 252 dots -> HBlank (mode 0)
	p.Tick(172)
	if m := statMode(p); m != 0 {
		t.Fatalf("expected mode 0 at dot 252, got %d", m)
	}
	// End of line -> next line mode 2 and LY increments
	p.Tick(456 - 252)
	if ly := p.CPURead(0xFF44); ly != 1 {
		t.Fatalf("expected LY=1, got %d", ly)
	}
	if m := statMode(p); m != 2 {
		t.Fatalf("expected mode 2 at new line, got %d", m)
	}
	_ = irqs
}

func TestPPUVBlankAndSTATOnVBlank(t *testing.T) {
	var got []int
	p := New(func(bit int) { got = append(got, bit) })
	// Enable STAT interrupt on VBlank (bit4)
	p.CPUWrite(0xFF41, 1<<4)
	// Turn LCD on
	p.CPUWrite(0xFF40, 0x80)
	// Advance to start of LY=144: 144 lines * 456 dots
	p.Tick(144 * 456)
	// Expect a VBlank IF (bit 0) and a STAT (bit 1)
	vb, st := 0, 0
	for _, b := range got {
		if b == 0 {
			vb++
		} else if b == 1 {
			st++
		}
	}
	if vb == 0 {
		t.Fatalf("expected at least one VBlank IRQ at LY=144")
	}
	if st == 0 {
		t.Fatalf("expected STAT IRQ on VBlank when enabled")
	}
}

func TestSTATModeAndLYCCoincidence(t *testing.T) {
	var got []int
	p := New(func(bit int) { got = append(got, bit) })
	// Enable STAT for HBlank (bit3), OAM (bit5), and LYC (bit6)
	p.CPUWrite(0xFF41, (1<<3)|(1<<5)|(1<<6))
	// Set LYC=2 to trigger coincidence on line 2
	p.CPUWrite(0xFF45, 2)
	// Turn LCD on
	p.CPUWrite(0xFF40, 0x80)
	// First line: mode 2->3->0 should trigger HBlank STAT once
	// Advance to HBlank of first line
	p.Tick(80 + 172) // now entering HBlank (mode 0)
	// One STAT due to HBlank expected
	hblankStats := 0
	for _, b := range got {
		if b == 1 {
			hblankStats++
		}
	}
	if hblankStats == 0 {
		t.Fatalf("expected STAT IRQ on HBlank when enabled")
	}
	// Clear and advance to LY=2 to test LYC coincidence
	got = got[:0]
	// Finish line 0, then full line 1, then start of line 2 to update LYC
	p.Tick((456 - (80 + 172)) + 456 + 1)
	// Expect a STAT due to LYC coincidence enable at LY==LYC
	hasLYC := false
	for _, b := range got {
		if b == 1 {
			hasLYC = true
			break
		}
	}
	if !hasLYC {
		t.Fatalf("expected STAT IRQ on LYC coincidence at LY=2")
	}
}
