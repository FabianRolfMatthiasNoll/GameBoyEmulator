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
    sampleRate       int
    cyclesPerSample  float64
    cycAccum         float64
    mixGain          float64

    // output ring buffer (mono int16 samples)
    buf     []int16
    bufHead int
    bufTail int

    // Mixing registers (not fully used yet)
    nr50 byte // 0xFF24
    nr51 byte // 0xFF25
    nr52 byte // 0xFF26 (power, channels status)

    // Channel 2 (NR21..NR24)
    ch2 chSquare
}

type chSquare struct {
    enabled bool
    duty    byte  // 0..3
    length  int   // 0..63
    lenEn   bool  // length enable
    vol     byte  // 0..15 initial volume (envelope ignored for now)
    envDir  int8  // +1/-1 (ignored for now)
    envPer  byte  // 0..7 (ignored for now)
    freq    uint16
    timer   int    // frequency timer in CPU cycles
    phase   int    // 0..7 index into duty pattern
}

var dutyTable = [4][8]byte{
    // 12.5%, 25%, 50%, 75% (pan docs pattern)
    {0,0,0,0,0,0,0,1},
    {1,0,0,0,0,0,0,1},
    {1,0,0,0,0,1,1,1},
    {0,1,1,1,1,1,1,0},
}

func New(sampleRate int) *APU {
    if sampleRate <= 0 { sampleRate = 48000 }
    a := &APU{
        enabled:        false,
        sampleRate:     sampleRate,
        cyclesPerSample: float64(cpuHz) / float64(sampleRate),
        mixGain:        0.25, // conservative
        buf:            make([]int16, 8192),
    }
    return a
}

// CPURead reads an APU register.
func (a *APU) CPURead(addr uint16) byte {
    switch addr {
    case 0xFF16: // NR21 duty/length
        return (a.ch2.duty << 6) | byte(0x3F - (a.ch2.length & 0x3F))
    case 0xFF17: // NR22 envelope
        dir := byte(0)
        if a.ch2.envDir > 0 { dir = 1 }
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
        if a.ch2.enabled { chFlags |= 1 << 1 }
        return 0x70 | (boolToByte(a.enabled) << 7) | chFlags
    default:
        return 0xFF
    }
}

// CPUWrite writes an APU register.
func (a *APU) CPUWrite(addr uint16, v byte) {
    switch addr {
    case 0xFF16: // NR21 duty/length
        a.ch2.duty = (v >> 6) & 3
        a.ch2.length = 64 - int(v&0x3F)
    case 0xFF17: // NR22 envelope
        a.ch2.vol = (v >> 4) & 0x0F
        if (v & (1<<3)) != 0 { a.ch2.envDir = 1 } else { a.ch2.envDir = -1 }
        a.ch2.envPer = v & 7
    case 0xFF18: // NR23
        a.ch2.freq = (a.ch2.freq & 0x0700) | uint16(v)
        a.reloadCh2Timer()
    case 0xFF19: // NR24
        a.ch2.lenEn = (v & (1<<6)) != 0
        a.ch2.freq = (a.ch2.freq & 0x00FF) | (uint16(v&7) << 8)
        if (v & (1<<7)) != 0 {
            a.triggerCh2()
        }
    case 0xFF24:
        a.nr50 = v
    case 0xFF25:
        a.nr51 = v
    case 0xFF26:
        // Power control
        pwr := (v & (1<<7)) != 0
        if !pwr {
            // power off clears state
            *a = *New(a.sampleRate)
            a.enabled = false
        } else {
            a.enabled = true
        }
    }
}

func (a *APU) triggerCh2() {
    a.ch2.enabled = true
    if a.ch2.length == 0 { a.ch2.length = 64 }
    a.ch2.phase = 0
    a.reloadCh2Timer()
}

func (a *APU) reloadCh2Timer() {
    periodCycles := int(4 * (2048 - (a.ch2.freq & 0x7FF)))
    if periodCycles < 8 { periodCycles = 8 }
    a.ch2.timer = periodCycles
}

// Tick advances the APU by the given number of CPU cycles, and pushes PCM samples when due.
func (a *APU) Tick(cycles int) {
    if cycles <= 0 { return }
    for i := 0; i < cycles; i++ {
        if a.enabled {
            // channel 2 timer and phase
            if a.ch2.enabled {
                a.ch2.timer--
                if a.ch2.timer <= 0 {
                    a.reloadCh2Timer()
                    a.ch2.phase = (a.ch2.phase + 1) & 7
                    // length counter
                    if a.ch2.lenEn && a.ch2.length > 0 {
                        // approx length clocking: decrement every full waveform cycle
                        // (rough but acceptable for first cut)
                        if a.ch2.phase == 0 {
                            a.ch2.length--
                            if a.ch2.length <= 0 { a.ch2.enabled = false }
                        }
                    }
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

func (a *APU) mixSample() int16 {
    // Channel 2 contribution
    var val float64
    if a.ch2.enabled {
        pat := dutyTable[a.ch2.duty]
        on := pat[a.ch2.phase] != 0
        amp := float64(a.ch2.vol) / 15.0
        if on { val += amp } else { val += 0 }
    }
    // apply conservative gain and convert to int16
    v := val * a.mixGain
    if v > 1 { v = 1 }
    if v < -1 { v = -1 }
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
    if max <= 0 || a.bufHead == a.bufTail { return nil }
    out := make([]int16, 0, max)
    for len(out) < max && a.bufTail != a.bufHead {
        out = append(out, a.buf[a.bufTail])
        a.bufTail = (a.bufTail + 1) & (len(a.buf) - 1)
    }
    return out
}

// --- Save/Load state ---
type apuState struct {
    Enabled bool
    NR50, NR51, NR52 byte
    Ch2 ch2State
    CycAccum float64
}

type ch2State struct {
    Enabled bool
    Duty byte
    Length int
    LenEn bool
    Vol byte
    EnvDir int8
    EnvPer byte
    Freq uint16
    Timer int
    Phase int
}

func (a *APU) SaveState() []byte {
    var buf bytes.Buffer
    enc := gob.NewEncoder(&buf)
    s := apuState{
        Enabled: a.enabled,
        NR50: a.nr50, NR51: a.nr51, NR52: a.nr52,
        Ch2: ch2State{
            Enabled: a.ch2.enabled, Duty: a.ch2.duty, Length: a.ch2.length,
            LenEn: a.ch2.lenEn, Vol: a.ch2.vol, EnvDir: a.ch2.envDir, EnvPer: a.ch2.envPer,
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
    if err := dec.Decode(&s); err != nil { return }
    a.enabled = s.Enabled
    a.nr50, a.nr51, a.nr52 = s.NR50, s.NR51, s.NR52
    a.ch2.enabled = s.Ch2.Enabled
    a.ch2.duty = s.Ch2.Duty
    a.ch2.length = s.Ch2.Length
    a.ch2.lenEn = s.Ch2.LenEn
    a.ch2.vol = s.Ch2.Vol
    a.ch2.envDir = s.Ch2.EnvDir
    a.ch2.envPer = s.Ch2.EnvPer
    a.ch2.freq = s.Ch2.Freq
    a.ch2.timer = s.Ch2.Timer
    a.ch2.phase = s.Ch2.Phase
    a.cycAccum = s.CycAccum
}

func boolToByte(b bool) byte { if b { return 1 } ; return 0 }
