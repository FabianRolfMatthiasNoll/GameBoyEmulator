package apu

import (
	"bytes"
	"encoding/gob"
)

// CPU frequency in Hz (DMG)
const cpuHz = 4194304

// APU is a DMG audio unit with channels 1, 2, 3, 4 implemented.
// It generates mono 16-bit samples into an internal ring buffer at the given sample rate.
type APU struct {
	enabled bool

	// sample generation
	sampleRate      int
	cyclesPerSample float64
	cycAccum        float64
	mixGain         float64

	// frame sequencer (512 Hz)
	fsCounter int // cycles until next step
	fsStep    int // 0..7

	// output ring buffer (mono int16 samples)
	buf     []int16
	bufHead int
	bufTail int

	// stereo ring buffers (left/right), capacity same power-of-two size
	sL    []int16
	sR    []int16
	sHead int
	sTail int

	// Mixing registers (not fully used yet)
	nr50 byte // 0xFF24
	nr51 byte // 0xFF25
	nr52 byte // 0xFF26 (power, channels status)

	// Channel 1 (NR10..NR14) - square with sweep
	ch1 chSquare
	// Channel 2 (NR21..NR24)
	ch2 chSquare
	// Channel 3 (NR30..NR34) - wave
	ch3 chWave
	// Channel 4 (NR41..NR44) - noise
	ch4 chNoise
}

type chSquare struct {
	enabled bool
	duty    byte // 0..3
	length  int  // 0..63
	lenEn   bool // length enable
	vol     byte // 0..15 initial volume
	envDir  int8 // +1/-1
	envPer  byte // 0..7 (0 means 8)
	curVol  byte // current envelope volume
	envTmr  byte // envelope timer
	freq    uint16
	timer   int // frequency timer in CPU cycles
	phase   int // 0..7 index into duty pattern

	// Sweep (used by CH1 only; safe to leave zero for others)
	sweepPer    byte
	sweepNeg    bool
	sweepShift  byte
	sweepTmr    byte
	sweepEn     bool
	sweepShadow uint16
}

type chWave struct {
	enabled bool
	dacEn   bool
	length  int // 0..255
	lenEn   bool
	volCode byte // 0..3 (0 mute, 1:100%, 2:50%, 3:25%)
	freq    uint16
	timer   int
	pos     int      // 0..31
	ram     [16]byte // FF30..FF3F (32 samples, 4-bit each)
}

type chNoise struct {
	enabled bool
	length  int
	lenEn   bool
	vol     byte
	envDir  int8
	envPer  byte
	curVol  byte
	envTmr  byte
	// NR43
	shift  byte // 0..15 shift clock frequency
	width7 bool // true for 7-bit LFSR; false for 15-bit
	divSel byte // 0..7 dividing ratio code
	timer  int
	lfsr   uint16 // 15-bit LFSR; bit0 is output
}

var dutyTable = [4][8]byte{
	// 12.5%, 25%, 50%, 75% (pan docs pattern)
	{0, 0, 0, 0, 0, 0, 0, 1},
	{1, 0, 0, 0, 0, 0, 0, 1},
	{1, 0, 0, 0, 0, 1, 1, 1},
	{0, 1, 1, 1, 1, 1, 1, 0},
}

func New(sampleRate int) *APU {
	if sampleRate <= 0 {
		sampleRate = 48000
	}
	a := &APU{
		enabled:         true,
		sampleRate:      sampleRate,
		cyclesPerSample: float64(cpuHz) / float64(sampleRate),
		mixGain:         0.20, // a bit more headroom to reduce clipping
		fsCounter:       cpuHz / 512,
		buf:             make([]int16, 16384),
		sL:              make([]int16, 16384),
		sR:              make([]int16, 16384),
	}
	// Sensible stereo defaults: route all channels to both and set max master volume.
	a.nr50 = 0x77
	a.nr51 = 0xFF
	return a
}

// CPURead reads an APU register.
func (a *APU) CPURead(addr uint16) byte {
	switch addr {
	case 0xFF10: // NR10 sweep (CH1)
		n := (a.ch1.sweepPer & 7) << 4
		if a.ch1.sweepNeg {
			n |= 1 << 3
		}
		n |= a.ch1.sweepShift & 7
		return 0x80 | n
	case 0xFF11: // NR11 duty/length (CH1)
		return (a.ch1.duty << 6) | byte(0x3F-(a.ch1.length&0x3F))
	case 0xFF12: // NR12 envelope (CH1)
		dir := byte(0)
		if a.ch1.envDir > 0 {
			dir = 1
		}
		return (a.ch1.vol << 4) | (dir << 3) | (a.ch1.envPer & 7)
	case 0xFF13: // NR13 freq lo (CH1)
		return byte(a.ch1.freq & 0xFF)
	case 0xFF14: // NR14 (CH1)
		return (boolToByte(a.ch1.lenEn) << 6) | byte((a.ch1.freq>>8)&7)
	case 0xFF16: // NR21 duty/length
		return (a.ch2.duty << 6) | byte(0x3F-(a.ch2.length&0x3F))
	case 0xFF17: // NR22 envelope
		dir := byte(0)
		if a.ch2.envDir > 0 {
			dir = 1
		}
		return (a.ch2.vol << 4) | (dir << 3) | (a.ch2.envPer & 7)
	case 0xFF18: // NR23 freq lo
		return byte(a.ch2.freq & 0xFF)
	case 0xFF19: // NR24
		return (boolToByte(a.ch2.lenEn) << 6) | byte((a.ch2.freq>>8)&7)
	case 0xFF1A: // NR30 (CH3 DAC)
		if a.ch3.dacEn {
			return 0x80
		} else {
			return 0x00
		}
	case 0xFF1B: // NR31 length (CH3)
		return byte(0xFF - (a.ch3.length & 0xFF))
	case 0xFF1C: // NR32 volume (CH3)
		return (a.ch3.volCode << 5) | 0x9F
	case 0xFF1D: // NR33 freq lo (CH3)
		return byte(a.ch3.freq & 0xFF)
	case 0xFF1E: // NR34 (CH3)
		return (boolToByte(a.ch3.lenEn) << 6) | byte((a.ch3.freq>>8)&7)
	case 0xFF30, 0xFF31, 0xFF32, 0xFF33, 0xFF34, 0xFF35, 0xFF36, 0xFF37,
		0xFF38, 0xFF39, 0xFF3A, 0xFF3B, 0xFF3C, 0xFF3D, 0xFF3E, 0xFF3F:
		return a.ch3.ram[addr-0xFF30]
	case 0xFF20: // NR41 length (CH4)
		return byte(0x3F - (a.ch4.length & 0x3F))
	case 0xFF21: // NR42 envelope (CH4)
		dir := byte(0)
		if a.ch4.envDir > 0 {
			dir = 1
		}
		return (a.ch4.vol << 4) | (dir << 3) | (a.ch4.envPer & 7)
	case 0xFF22: // NR43 poly counter (CH4)
		w := byte(0)
		if a.ch4.width7 {
			w = 1
		}
		return (a.ch4.shift << 4) | (w << 3) | (a.ch4.divSel & 7)
	case 0xFF23: // NR44 (CH4)
		return (boolToByte(a.ch4.lenEn) << 6)
	case 0xFF24:
		return a.nr50
	case 0xFF25:
		return a.nr51
	case 0xFF26:
		// Bit 7 = power; bits 0-3 channel on flags (we expose ch2 as bit1)
		chFlags := byte(0)
		if a.ch1.enabled {
			chFlags |= 1 << 0
		}
		if a.ch2.enabled {
			chFlags |= 1 << 1
		}
		if a.ch3.enabled {
			chFlags |= 1 << 2
		}
		if a.ch4.enabled {
			chFlags |= 1 << 3
		}
		return 0x70 | (boolToByte(a.enabled) << 7) | chFlags
	default:
		return 0xFF
	}
}

// CPUWrite writes an APU register.
func (a *APU) CPUWrite(addr uint16, v byte) {
	switch addr {
	case 0xFF10: // NR10 (CH1 sweep)
		a.ch1.sweepPer = (v >> 4) & 7
		a.ch1.sweepNeg = (v & (1 << 3)) != 0
		a.ch1.sweepShift = v & 7
	case 0xFF11: // NR11 (CH1 duty/length)
		a.ch1.duty = (v >> 6) & 3
		a.ch1.length = 64 - int(v&0x3F)
	case 0xFF12: // NR12 (CH1 envelope)
		a.ch1.vol = (v >> 4) & 0x0F
		if (v & (1 << 3)) != 0 {
			a.ch1.envDir = 1
		} else {
			a.ch1.envDir = -1
		}
		a.ch1.envPer = v & 7
		// DAC off if upper 5 bits are zero; if so, disable channel
		if (v & 0xF8) == 0 {
			a.ch1.enabled = false
		}
	case 0xFF13: // NR13 (CH1 freq lo)
		a.ch1.freq = (a.ch1.freq & 0x0700) | uint16(v)
		a.reloadCh1Timer()
	case 0xFF14: // NR14 (CH1)
		a.ch1.lenEn = (v & (1 << 6)) != 0
		a.ch1.freq = (a.ch1.freq & 0x00FF) | (uint16(v&7) << 8)
		if (v & (1 << 7)) != 0 {
			a.triggerCh1()
		}
	case 0xFF16: // NR21 duty/length
		a.ch2.duty = (v >> 6) & 3
		a.ch2.length = 64 - int(v&0x3F)
	case 0xFF17: // NR22 envelope
		a.ch2.vol = (v >> 4) & 0x0F
		if (v & (1 << 3)) != 0 {
			a.ch2.envDir = 1
		} else {
			a.ch2.envDir = -1
		}
		a.ch2.envPer = v & 7
		if (v & 0xF8) == 0 { // DAC off -> channel off
			a.ch2.enabled = false
		}
	case 0xFF18: // NR23
		a.ch2.freq = (a.ch2.freq & 0x0700) | uint16(v)
		a.reloadCh2Timer()
	case 0xFF19: // NR24
		a.ch2.lenEn = (v & (1 << 6)) != 0
		a.ch2.freq = (a.ch2.freq & 0x00FF) | (uint16(v&7) << 8)
		if (v & (1 << 7)) != 0 {
			a.triggerCh2()
		}
	case 0xFF1A: // NR30 (CH3 DAC)
		a.ch3.dacEn = (v & 0x80) != 0
		if !a.ch3.dacEn {
			a.ch3.enabled = false
		}
	case 0xFF1B: // NR31 (CH3 length)
		a.ch3.length = 256 - int(v)
	case 0xFF1C: // NR32 (CH3 volume)
		a.ch3.volCode = (v >> 5) & 3
	case 0xFF1D: // NR33 (CH3 freq lo)
		a.ch3.freq = (a.ch3.freq & 0x0700) | uint16(v)
		a.reloadCh3Timer()
	case 0xFF1E: // NR34 (CH3)
		a.ch3.lenEn = (v & (1 << 6)) != 0
		a.ch3.freq = (a.ch3.freq & 0x00FF) | (uint16(v&7) << 8)
		if (v & (1 << 7)) != 0 {
			a.triggerCh3()
		}
	case 0xFF30, 0xFF31, 0xFF32, 0xFF33, 0xFF34, 0xFF35, 0xFF36, 0xFF37,
		0xFF38, 0xFF39, 0xFF3A, 0xFF3B, 0xFF3C, 0xFF3D, 0xFF3E, 0xFF3F:
		a.ch3.ram[addr-0xFF30] = v
	case 0xFF24:
		a.nr50 = v
	case 0xFF25:
		a.nr51 = v
	case 0xFF26:
		// Power control
		pwr := (v & (1 << 7)) != 0
		if !pwr {
			// power off clears state
			*a = *New(a.sampleRate)
			a.enabled = false
		} else {
			a.enabled = true
		}
	case 0xFF20: // NR41 (CH4 length)
		a.ch4.length = 64 - int(v&0x3F)
	case 0xFF21: // NR42 (CH4 envelope)
		a.ch4.vol = (v >> 4) & 0x0F
		if (v & (1 << 3)) != 0 {
			a.ch4.envDir = 1
		} else {
			a.ch4.envDir = -1
		}
		a.ch4.envPer = v & 7
		if (v & 0xF8) == 0 {
			a.ch4.enabled = false
		}
	case 0xFF22: // NR43 (CH4 polynomial)
		a.ch4.shift = (v >> 4) & 0x0F
		a.ch4.width7 = (v & (1 << 3)) != 0
		a.ch4.divSel = v & 7
		a.reloadCh4Timer()
	case 0xFF23: // NR44 (CH4)
		a.ch4.lenEn = (v & (1 << 6)) != 0
		if (v & (1 << 7)) != 0 {
			a.triggerCh4()
		}
	}
}

func (a *APU) triggerCh1() {
	// If DAC off (NR12 upper 5 bits = 0), channel stays disabled
	if a.ch1.vol == 0 && a.ch1.envDir < 0 { // simple DAC check approximation
		a.ch1.enabled = false
	} else {
		a.ch1.enabled = true
	}
	if a.ch1.length == 0 {
		a.ch1.length = 64
	}
	a.ch1.phase = 0
	a.reloadCh1Timer()
	// Envelope
	a.ch1.curVol = a.ch1.vol
	per := a.ch1.envPer
	if per == 0 {
		per = 8
	}
	a.ch1.envTmr = per
	// Sweep
	a.ch1.sweepShadow = a.ch1.freq & 0x7FF
	a.ch1.sweepEn = (a.ch1.sweepPer != 0) || (a.ch1.sweepShift != 0)
	st := a.ch1.sweepPer
	if st == 0 {
		st = 8
	}
	a.ch1.sweepTmr = st
	if a.ch1.sweepShift != 0 {
		// Pre-calc overflow check
		if a.calcCh1Sweep(true) > 2047 {
			a.ch1.enabled = false
		}
	}
}

func (a *APU) triggerCh2() {
	// If DAC off (NR22 upper 5 bits = 0), do not enable
	if a.ch2.vol == 0 && a.ch2.envDir < 0 {
		a.ch2.enabled = false
		return
	}
	a.ch2.enabled = true
	if a.ch2.length == 0 {
		a.ch2.length = 64
	}
	a.ch2.phase = 0
	a.reloadCh2Timer()
	// Envelope
	a.ch2.curVol = a.ch2.vol
	per := a.ch2.envPer
	if per == 0 {
		per = 8
	}
	a.ch2.envTmr = per
}

func (a *APU) reloadCh1Timer() {
	periodCycles := int(4 * (2048 - (a.ch1.freq & 0x7FF)))
	if periodCycles < 8 {
		periodCycles = 8
	}
	a.ch1.timer = periodCycles
}

func (a *APU) reloadCh2Timer() {
	periodCycles := int(4 * (2048 - (a.ch2.freq & 0x7FF)))
	if periodCycles < 8 {
		periodCycles = 8
	}
	a.ch2.timer = periodCycles
}

func (a *APU) reloadCh3Timer() {
	periodCycles := int(2 * (2048 - (a.ch3.freq & 0x7FF)))
	if periodCycles < 2 {
		periodCycles = 2
	}
	a.ch3.timer = periodCycles
}

func (a *APU) triggerCh3() {
	if !a.ch3.dacEn {
		a.ch3.enabled = false
	} else {
		a.ch3.enabled = true
	}
	if a.ch3.length == 0 {
		a.ch3.length = 256
	}
	a.ch3.pos = 0
	a.reloadCh3Timer()
}

func (a *APU) triggerCh4() {
	// DAC off check: if initial volume is 0 and decreasing, mute
	if a.ch4.vol == 0 && a.ch4.envDir < 0 {
		a.ch4.enabled = false
	} else {
		a.ch4.enabled = true
	}
	if a.ch4.length == 0 {
		a.ch4.length = 64
	}
	a.ch4.curVol = a.ch4.vol
	per := a.ch4.envPer
	if per == 0 {
		per = 8
	}
	a.ch4.envTmr = per
	a.ch4.lfsr = 0x7FFF
	a.reloadCh4Timer()
}

func (a *APU) reloadCh4Timer() {
	// Divisor table for CH4 dividing ratio
	divTable := [8]int{8, 16, 32, 48, 64, 80, 96, 112}
	div := divTable[int(a.ch4.divSel&7)]
	// cycles per step ≈ divisor << (shift+4)
	period := div << (int(a.ch4.shift) + 4)
	if period < 2 {
		period = 2
	}
	a.ch4.timer = period
}

// Tick advances the APU by the given number of CPU cycles, and pushes PCM samples when due.
func (a *APU) Tick(cycles int) {
	if cycles <= 0 {
		return
	}
	for i := 0; i < cycles; i++ {
		if a.enabled {
			// frame sequencer @ 512 Hz
			a.fsCounter--
			if a.fsCounter <= 0 {
				a.fsCounter += cpuHz / 512
				a.fsStep = (a.fsStep + 1) & 7
				// Length clock on steps 0,2,4,6
				if a.fsStep%2 == 0 {
					a.clockLength()
				}
				// Sweep on steps 2,6
				if a.fsStep == 2 || a.fsStep == 6 {
					a.clockSweep()
				}
				// Envelope on step 7
				if a.fsStep == 7 {
					a.clockEnvelope()
				}
			}
			// channel timers and phase
			if a.ch1.enabled {
				a.ch1.timer--
				if a.ch1.timer <= 0 {
					a.reloadCh1Timer()
					a.ch1.phase = (a.ch1.phase + 1) & 7
				}
			}
			if a.ch3.enabled {
				a.ch3.timer--
				if a.ch3.timer <= 0 {
					a.reloadCh3Timer()
					a.ch3.pos = (a.ch3.pos + 1) & 31
				}
			}
			if a.ch2.enabled {
				a.ch2.timer--
				if a.ch2.timer <= 0 {
					a.reloadCh2Timer()
					a.ch2.phase = (a.ch2.phase + 1) & 7
				}
			}
			// channel 4 timer and LFSR
			if a.ch4.enabled {
				a.ch4.timer--
				if a.ch4.timer <= 0 {
					a.reloadCh4Timer()
					// XOR of bit0 and bit1, then shift right
					x := (a.ch4.lfsr ^ (a.ch4.lfsr >> 1)) & 1
					a.ch4.lfsr >>= 1
					a.ch4.lfsr |= (x << 14)
					if a.ch4.width7 {
						// also put into bit6 for 7-bit mode
						a.ch4.lfsr &^= 1 << 6
						a.ch4.lfsr |= (x << 6)
					}
				}
			}
			// sample generation
			a.cycAccum += 1
			for a.cycAccum >= a.cyclesPerSample {
				a.cycAccum -= a.cyclesPerSample
				l, r := a.mixSampleStereo()
				a.pushStereo(l, r)
				// keep mono buffer in sync by averaging for backward compatibility
				avg := int32(l) + int32(r)
				a.pushSample(int16(avg / 2))
			}
		}
	}
}

func (a *APU) clockLength() {
	if a.ch1.lenEn && a.ch1.length > 0 {
		a.ch1.length--
		if a.ch1.length <= 0 {
			a.ch1.enabled = false
		}
	}
	if a.ch3.lenEn && a.ch3.length > 0 {
		a.ch3.length--
		if a.ch3.length <= 0 {
			a.ch3.enabled = false
		}
	}
	if a.ch2.lenEn && a.ch2.length > 0 {
		a.ch2.length--
		if a.ch2.length <= 0 {
			a.ch2.enabled = false
		}
	}
}

func (a *APU) clockEnvelope() {
	// CH1
	if a.ch1.enabled && a.ch1.envPer != 0 {
		if a.ch1.envTmr > 0 {
			a.ch1.envTmr--
		}
		if a.ch1.envTmr == 0 {
			a.ch1.envTmr = a.ch1.envPer
			if a.ch1.envDir > 0 && a.ch1.curVol < 15 {
				a.ch1.curVol++
			} else if a.ch1.envDir < 0 && a.ch1.curVol > 0 {
				a.ch1.curVol--
			}
		}
	}
	// CH2
	if a.ch2.enabled && a.ch2.envPer != 0 {
		if a.ch2.envTmr > 0 {
			a.ch2.envTmr--
		}
		if a.ch2.envTmr == 0 {
			a.ch2.envTmr = a.ch2.envPer
			if a.ch2.envDir > 0 && a.ch2.curVol < 15 {
				a.ch2.curVol++
			} else if a.ch2.envDir < 0 && a.ch2.curVol > 0 {
				a.ch2.curVol--
			}
		}
	}
	// CH4
	if a.ch4.enabled && a.ch4.envPer != 0 {
		if a.ch4.envTmr > 0 {
			a.ch4.envTmr--
		}
		if a.ch4.envTmr == 0 {
			a.ch4.envTmr = a.ch4.envPer
			if a.ch4.envDir > 0 && a.ch4.curVol < 15 {
				a.ch4.curVol++
			} else if a.ch4.envDir < 0 && a.ch4.curVol > 0 {
				a.ch4.curVol--
			}
		}
	}
}

func (a *APU) clockSweep() {
	if !a.ch1.enabled || !a.ch1.sweepEn || a.ch1.sweepPer == 0 {
		return
	}
	if a.ch1.sweepTmr > 0 {
		a.ch1.sweepTmr--
	}
	if a.ch1.sweepTmr == 0 {
		// reload timer
		a.ch1.sweepTmr = a.ch1.sweepPer
		nf := a.calcCh1Sweep(true)
		if nf > 2047 {
			a.ch1.enabled = false
		} else {
			a.ch1.sweepShadow = uint16(nf)
			a.ch1.freq = (a.ch1.freq &^ 0x07FF) | (uint16(nf) & 0x07FF)
			a.reloadCh1Timer()
			// second overflow check
			if a.calcCh1Sweep(false) > 2047 {
				a.ch1.enabled = false
			}
		}
	}
}

func (a *APU) calcCh1Sweep(applyShift bool) int {
	base := int(a.ch1.sweepShadow)
	if a.ch1.sweepShift == 0 {
		return base
	}
	delta := base >> a.ch1.sweepShift
	if a.ch1.sweepNeg {
		return base - delta
	}
	if applyShift {
		return base + delta
	}
	return base + delta
}

func (a *APU) mixSample() int16 {
	// Channel 2 contribution
	var val float64
	if a.ch1.enabled {
		pat := dutyTable[a.ch1.duty]
		on := pat[a.ch1.phase] != 0
		amp := float64(a.ch1.curVol) / 15.0
		if on {
			val += amp
		} else {
			val -= amp
		}
	}
	// CH3 contribution
	if a.ch3.enabled && a.ch3.dacEn {
		b := a.ch3.ram[a.ch3.pos>>1]
		var n4 byte
		if (a.ch3.pos & 1) == 0 {
			n4 = (b >> 4) & 0x0F
		} else {
			n4 = b & 0x0F
		}
		if a.ch3.volCode != 0 {
			shift := a.ch3.volCode - 1
			scaled := float64(n4 >> shift)
			max := float64(int(15) >> shift)
			if max < 1 {
				max = 1
			}
			// center around 0: 0..max -> -1..+1
			s := (scaled/max)*2.0 - 1.0
			val += s
		}
	}
	if a.ch2.enabled {
		pat := dutyTable[a.ch2.duty]
		on := pat[a.ch2.phase] != 0
		amp := float64(a.ch2.curVol) / 15.0
		if on {
			val += amp
		} else {
			val -= amp
		}
	}
	// CH4 contribution
	if a.ch4.enabled {
		on := ((^a.ch4.lfsr) & 1) != 0
		amp := float64(a.ch4.curVol) / 15.0
		if on {
			val += amp
		} else {
			val -= amp
		}
	}
	// apply conservative gain and convert to int16
	v := val * a.mixGain
	if v > 1 {
		v = 1
	}
	if v < -1 {
		v = -1
	}
	return int16(v * 32767)
}

// mixSampleStereo computes one stereo sample pair according to NR50/NR51.
func (a *APU) mixSampleStereo() (int16, int16) {
	// Per-channel instantaneous values in [-1, +1]
	c1, c2, c3, c4 := 0.0, 0.0, 0.0, 0.0
	if a.ch1.enabled {
		pat := dutyTable[a.ch1.duty]
		on := pat[a.ch1.phase] != 0
		amp := float64(a.ch1.curVol) / 15.0
		if on {
			c1 += amp
		} else {
			c1 -= amp
		}
	}
	if a.ch2.enabled {
		pat := dutyTable[a.ch2.duty]
		on := pat[a.ch2.phase] != 0
		amp := float64(a.ch2.curVol) / 15.0
		if on {
			c2 += amp
		} else {
			c2 -= amp
		}
	}
	if a.ch3.enabled && a.ch3.dacEn {
		b := a.ch3.ram[a.ch3.pos>>1]
		var n4 byte
		if (a.ch3.pos & 1) == 0 {
			n4 = (b >> 4) & 0x0F
		} else {
			n4 = b & 0x0F
		}
		if a.ch3.volCode != 0 {
			shift := a.ch3.volCode - 1
			scaled := float64(n4 >> shift)
			max := float64(int(15) >> shift)
			if max < 1 {
				max = 1
			}
			// center around 0 with correct dynamic range based on volume code
			c3 += (scaled/max)*2.0 - 1.0
		}
	}
	if a.ch4.enabled {
		on := ((^a.ch4.lfsr) & 1) != 0
		amp := float64(a.ch4.curVol) / 15.0
		if on {
			c4 += amp
		} else {
			c4 -= amp
		}
	}
	// Routing via NR51: lower nibble = right (SO1), upper nibble = left (SO2)
	rMask := a.nr51 & 0x0F
	lMask := (a.nr51 >> 4) & 0x0F
	// Safety: some titles (or boot sequences) leave NR51=0 briefly; route all to both to avoid total silence.
	if rMask == 0 && lMask == 0 {
		rMask, lMask = 0x0F, 0x0F
	}
	l, r := 0.0, 0.0
	if (lMask & 0x1) != 0 {
		l += c1
	}
	if (lMask & 0x2) != 0 {
		l += c2
	}
	if (lMask & 0x4) != 0 {
		l += c3
	}
	if (lMask & 0x8) != 0 {
		l += c4
	}
	if (rMask & 0x1) != 0 {
		r += c1
	}
	if (rMask & 0x2) != 0 {
		r += c2
	}
	if (rMask & 0x4) != 0 {
		r += c3
	}
	if (rMask & 0x8) != 0 {
		r += c4
	}
	// Master volumes via NR50: SO1(right) level bits 2-0, SO2(left) bits 6-4
	// Hardware: levels 0..7 map to 0..1 linearly (0 is silence)
	rv := float64(a.nr50&0x07) / 7.0
	lv := float64((a.nr50>>4)&0x07) / 7.0
	l *= lv
	r *= rv
	// Apply overall gain and clamp
	l *= a.mixGain
	r *= a.mixGain
	if l > 1 {
		l = 1
	} else if l < -1 {
		l = -1
	}
	if r > 1 {
		r = 1
	} else if r < -1 {
		r = -1
	}
	return int16(l * 32767), int16(r * 32767)
}

// pushStereo pushes a stereo frame to the ring buffers.
func (a *APU) pushStereo(l, r int16) {
	next := (a.sHead + 1) & (len(a.sL) - 1)
	if next == a.sTail {
		return // drop if full
	}
	a.sL[a.sHead] = l
	a.sR[a.sHead] = r
	a.sHead = next
}

// PullStereo returns up to max stereo frames as an interleaved int16 slice [L0,R0,L1,R1,...].
func (a *APU) PullStereo(max int) []int16 {
	if max <= 0 || a.sHead == a.sTail {
		return nil
	}
	// Limit by available frames
	count := 0
	for i := a.sTail; i != a.sHead && count < max; i = (i + 1) & (len(a.sL) - 1) {
		count++
	}
	out := make([]int16, 0, count*2)
	for i := 0; i < count; i++ {
		out = append(out, a.sL[a.sTail], a.sR[a.sTail])
		a.sTail = (a.sTail + 1) & (len(a.sL) - 1)
	}
	return out
}

// StereoAvailable returns the number of stereo frames currently buffered.
func (a *APU) StereoAvailable() int {
	if a.sHead == a.sTail {
		return 0
	}
	if a.sHead >= a.sTail {
		return a.sHead - a.sTail
	}
	return (len(a.sL) - a.sTail) + a.sHead
}

func (a *APU) pushSample(s int16) {
	next := (a.bufHead + 1) & (len(a.buf) - 1)
	if next == a.bufTail {
		// buffer full, drop sample
		return
	}
	a.buf[a.bufHead] = s
	a.bufHead = next
}

// PullSamples copies up to max samples out of the ring buffer.
func (a *APU) PullSamples(max int) []int16 {
	if max <= 0 || a.bufHead == a.bufTail {
		return nil
	}
	out := make([]int16, 0, max)
	for len(out) < max && a.bufTail != a.bufHead {
		out = append(out, a.buf[a.bufTail])
		a.bufTail = (a.bufTail + 1) & (len(a.buf) - 1)
	}
	return out
}

// --- Save/Load state ---
type apuState struct {
	Enabled          bool
	NR50, NR51, NR52 byte
	FSctr            int
	FSstep           int
	Ch1              ch1State
	Ch2              ch2State
	Ch3              ch3State
	Ch4              ch4State
	CycAccum         float64
}

type ch1State struct {
	Enabled     bool
	Duty        byte
	Length      int
	LenEn       bool
	Vol         byte
	EnvDir      int8
	EnvPer      byte
	CurVol      byte
	EnvTmr      byte
	Freq        uint16
	Timer       int
	Phase       int
	SweepPer    byte
	SweepNeg    bool
	SweepShift  byte
	SweepTmr    byte
	SweepEn     bool
	SweepShadow uint16
}

type ch2State struct {
	Enabled bool
	Duty    byte
	Length  int
	LenEn   bool
	Vol     byte
	EnvDir  int8
	EnvPer  byte
	CurVol  byte
	EnvTmr  byte
	Freq    uint16
	Timer   int
	Phase   int
}

type ch3State struct {
	Enabled bool
	DAC     bool
	Length  int
	LenEn   bool
	VolCode byte
	Freq    uint16
	Timer   int
	Pos     int
	RAM     [16]byte
}

type ch4State struct {
	Enabled bool
	Length  int
	LenEn   bool
	Vol     byte
	EnvDir  int8
	EnvPer  byte
	CurVol  byte
	EnvTmr  byte
	Shift   byte
	Width7  bool
	DivSel  byte
	Timer   int
	LFSR    uint16
}

func (a *APU) SaveState() []byte {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	s := apuState{
		Enabled: a.enabled,
		NR50:    a.nr50, NR51: a.nr51, NR52: a.nr52,
		FSctr: a.fsCounter, FSstep: a.fsStep,
		Ch1: ch1State{
			Enabled: a.ch1.enabled, Duty: a.ch1.duty, Length: a.ch1.length,
			LenEn: a.ch1.lenEn, Vol: a.ch1.vol, EnvDir: a.ch1.envDir, EnvPer: a.ch1.envPer,
			CurVol: a.ch1.curVol, EnvTmr: a.ch1.envTmr,
			Freq: a.ch1.freq, Timer: a.ch1.timer, Phase: a.ch1.phase,
			SweepPer: a.ch1.sweepPer, SweepNeg: a.ch1.sweepNeg, SweepShift: a.ch1.sweepShift,
			SweepTmr: a.ch1.sweepTmr, SweepEn: a.ch1.sweepEn, SweepShadow: a.ch1.sweepShadow,
		},
		Ch2: ch2State{
			Enabled: a.ch2.enabled, Duty: a.ch2.duty, Length: a.ch2.length,
			LenEn: a.ch2.lenEn, Vol: a.ch2.vol, EnvDir: a.ch2.envDir, EnvPer: a.ch2.envPer,
			CurVol: a.ch2.curVol, EnvTmr: a.ch2.envTmr,
			Freq: a.ch2.freq, Timer: a.ch2.timer, Phase: a.ch2.phase,
		},
		Ch3: ch3State{
			Enabled: a.ch3.enabled, DAC: a.ch3.dacEn, Length: a.ch3.length, LenEn: a.ch3.lenEn,
			VolCode: a.ch3.volCode, Freq: a.ch3.freq, Timer: a.ch3.timer, Pos: a.ch3.pos,
			RAM: a.ch3.ram,
		},
		Ch4: ch4State{
			Enabled: a.ch4.enabled, Length: a.ch4.length, LenEn: a.ch4.lenEn,
			Vol: a.ch4.vol, EnvDir: a.ch4.envDir, EnvPer: a.ch4.envPer,
			CurVol: a.ch4.curVol, EnvTmr: a.ch4.envTmr,
			Shift: a.ch4.shift, Width7: a.ch4.width7, DivSel: a.ch4.divSel,
			Timer: a.ch4.timer, LFSR: a.ch4.lfsr,
		},
		CycAccum: a.cycAccum,
	}
	_ = enc.Encode(s)
	return buf.Bytes()
}

func (a *APU) LoadState(data []byte) {
	var s apuState
	dec := gob.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&s); err != nil {
		return
	}
	a.enabled = s.Enabled
	a.nr50, a.nr51, a.nr52 = s.NR50, s.NR51, s.NR52
	a.fsCounter, a.fsStep = s.FSctr, s.FSstep
	a.ch1.enabled = s.Ch1.Enabled
	a.ch1.duty = s.Ch1.Duty
	a.ch1.length = s.Ch1.Length
	a.ch1.lenEn = s.Ch1.LenEn
	a.ch1.vol = s.Ch1.Vol
	a.ch1.envDir = s.Ch1.EnvDir
	a.ch1.envPer = s.Ch1.EnvPer
	a.ch1.curVol = s.Ch1.CurVol
	a.ch1.envTmr = s.Ch1.EnvTmr
	a.ch1.freq = s.Ch1.Freq
	a.ch1.timer = s.Ch1.Timer
	a.ch1.phase = s.Ch1.Phase
	a.ch1.sweepPer = s.Ch1.SweepPer
	a.ch1.sweepNeg = s.Ch1.SweepNeg
	a.ch1.sweepShift = s.Ch1.SweepShift
	a.ch1.sweepTmr = s.Ch1.SweepTmr
	a.ch1.sweepEn = s.Ch1.SweepEn
	a.ch1.sweepShadow = s.Ch1.SweepShadow
	a.ch2.enabled = s.Ch2.Enabled
	a.ch2.duty = s.Ch2.Duty
	a.ch2.length = s.Ch2.Length
	a.ch2.lenEn = s.Ch2.LenEn
	a.ch2.vol = s.Ch2.Vol
	a.ch2.envDir = s.Ch2.EnvDir
	a.ch2.envPer = s.Ch2.EnvPer
	a.ch2.curVol = s.Ch2.CurVol
	a.ch2.envTmr = s.Ch2.EnvTmr
	a.ch2.freq = s.Ch2.Freq
	a.ch2.timer = s.Ch2.Timer
	a.ch2.phase = s.Ch2.Phase
	// CH3
	a.ch3.enabled = s.Ch3.Enabled
	a.ch3.dacEn = s.Ch3.DAC
	a.ch3.length = s.Ch3.Length
	a.ch3.lenEn = s.Ch3.LenEn
	a.ch3.volCode = s.Ch3.VolCode
	a.ch3.freq = s.Ch3.Freq
	a.ch3.timer = s.Ch3.Timer
	a.ch3.pos = s.Ch3.Pos
	a.ch3.ram = s.Ch3.RAM
	// CH4
	a.ch4.enabled = s.Ch4.Enabled
	a.ch4.length = s.Ch4.Length
	a.ch4.lenEn = s.Ch4.LenEn
	a.ch4.vol = s.Ch4.Vol
	a.ch4.envDir = s.Ch4.EnvDir
	a.ch4.envPer = s.Ch4.EnvPer
	a.ch4.curVol = s.Ch4.CurVol
	a.ch4.envTmr = s.Ch4.EnvTmr
	a.ch4.shift = s.Ch4.Shift
	a.ch4.width7 = s.Ch4.Width7
	a.ch4.divSel = s.Ch4.DivSel
	a.ch4.timer = s.Ch4.Timer
	a.ch4.lfsr = s.Ch4.LFSR
	a.cycAccum = s.CycAccum
}

func boolToByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}
