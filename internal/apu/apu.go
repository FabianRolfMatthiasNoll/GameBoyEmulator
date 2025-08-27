package apu

import (
	"bytes"
	"encoding/gob"
)

// CPU frequency in Hz (DMG)
const cpuHz = 4194304

// APU is a minimal DMG audio unit with Channel 2 implemented (square without sweep).
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

	// Mixing registers (not fully used yet)
	nr50 byte // 0xFF24
	nr51 byte // 0xFF25
	nr52 byte // 0xFF26 (power, channels status)

	// Channel 1 (NR10..NR14) - square with sweep
	ch1 chSquare
	// Channel 2 (NR21..NR24)
	ch2 chSquare
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
		enabled:         false,
		sampleRate:      sampleRate,
		cyclesPerSample: float64(cpuHz) / float64(sampleRate),
		mixGain:         0.25, // conservative
		fsCounter:       cpuHz / 512,
		buf:             make([]int16, 8192),
	}
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
	case 0xFF18: // NR23
		a.ch2.freq = (a.ch2.freq & 0x0700) | uint16(v)
		a.reloadCh2Timer()
	case 0xFF19: // NR24
		a.ch2.lenEn = (v & (1 << 6)) != 0
		a.ch2.freq = (a.ch2.freq & 0x00FF) | (uint16(v&7) << 8)
		if (v & (1 << 7)) != 0 {
			a.triggerCh2()
		}
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
			// channel 2 timer and phase
			if a.ch1.enabled {
				a.ch1.timer--
				if a.ch1.timer <= 0 {
					a.reloadCh1Timer()
					a.ch1.phase = (a.ch1.phase + 1) & 7
				}
			}
			if a.ch2.enabled {
				a.ch2.timer--
				if a.ch2.timer <= 0 {
					a.reloadCh2Timer()
					a.ch2.phase = (a.ch2.phase + 1) & 7
				}
			}
			// sample generation
			a.cycAccum += 1
			for a.cycAccum >= a.cyclesPerSample {
				a.cycAccum -= a.cyclesPerSample
				s := a.mixSample()
				a.pushSample(s)
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
		}
	}
	if a.ch2.enabled {
		pat := dutyTable[a.ch2.duty]
		on := pat[a.ch2.phase] != 0
		amp := float64(a.ch2.curVol) / 15.0
		if on {
			val += amp
		} else {
			val += 0
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
	a.cycAccum = s.CycAccum
}

func boolToByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}
