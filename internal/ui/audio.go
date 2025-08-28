package ui

import (
	"encoding/binary"
	"time"

	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/emu"
)

// applyPlayerBufferSize sets the audio player's internal buffer to a small size for low latency.
// Ebiten exposes Player.SetBufferSize; we pick:
// - ~20ms in low-latency (or during fast-forward)
// - ~40ms otherwise
func (a *App) applyPlayerBufferSize() {
	if a.audioPlayer == nil {
		return
	}
	bufMs := 40
	if a.cfg.AudioLowLatency || a.fast {
		bufMs = 20
	}
	a.audioPlayer.SetBufferSize(time.Duration(bufMs) * time.Millisecond)
}

// apuStream implements io.Reader by pulling PCM samples from the emulator APU and
// converting them to 16-bit little-endian stereo frames.
type apuStream struct {
	m          *emu.Machine
	mono       bool
	muted      *bool
	lowLatency bool
	// stats
	underruns  int
	lastWant   int
	lastPulled int
}

func (s *apuStream) Read(p []byte) (int, error) {
	if len(p) == 0 || s == nil || s.m == nil {
		return 0, nil
	}
	// If buffer is smaller than a full stereo frame (4 bytes), fill with silence to avoid returning 0 bytes.
	if len(p) < 4 {
		for i := range p {
			p[i] = 0
		}
		return len(p), nil
	}
	if s.muted != nil && *s.muted {
		for i := range p {
			p[i] = 0
		}
		time.Sleep(5 * time.Millisecond)
		return len(p), nil
	}
	// Each frame is 4 bytes (stereo int16). Limit per-read to a small cap to avoid over-buffering.
	maxReq := len(p) / 4
	capFrames := 2048 // ~42.7ms at 48kHz
	if s.lowLatency {
		capFrames = 1024 // ~21.3ms
	}
	if maxReq > capFrames {
		maxReq = capFrames
	}

	// Prefer to read only what's currently buffered to avoid padding, with a short wait.
	waitDur := 15 * time.Millisecond
	if s.lowLatency {
		waitDur = 8 * time.Millisecond
	}
	deadline := time.Now().Add(waitDur)
	want := maxReq
	if buf := s.m.APUBufferedStereo(); buf > 0 {
		if buf < want {
			want = buf
		}
	} else {
		// No data buffered yet: wait briefly for some to arrive
		for time.Now().Before(deadline) {
			if b := s.m.APUBufferedStereo(); b > 0 {
				want = b
				if want > maxReq {
					want = maxReq
				}
				break
			}
			time.Sleep(1 * time.Millisecond)
		}
	}
	if want <= 0 { // still nothing: return a minimal silence chunk (counts as underrun)
		silenceFrames := 256
		if silenceFrames > maxReq {
			silenceFrames = maxReq
		}
		for i := 0; i < silenceFrames*4 && i+3 < len(p); i += 4 {
			binary.LittleEndian.PutUint16(p[i:], 0)
			binary.LittleEndian.PutUint16(p[i+2:], 0)
		}
		s.underruns++
		s.lastWant = silenceFrames
		s.lastPulled = silenceFrames
		return silenceFrames * 4, nil
	}

	// Pull and convert exactly 'want' frames. Do not pad beyond what we pulled.
	pulled := 0
	i := 0
	for pulled < want {
		frames := s.m.APUPullStereo(want - pulled)
		if len(frames) == 0 {
			break
		}
		// Convert pulled frames
		for j := 0; j+1 < len(frames) && i+3 < len(p); j += 2 {
			l := int16(frames[j])
			r := int16(frames[j+1])
			if s.mono {
				m := int16((int32(l) + int32(r)) / 2)
				binary.LittleEndian.PutUint16(p[i:], uint16(m))
				binary.LittleEndian.PutUint16(p[i+2:], uint16(m))
			} else {
				binary.LittleEndian.PutUint16(p[i:], uint16(l))
				binary.LittleEndian.PutUint16(p[i+2:], uint16(r))
			}
			i += 4
			pulled++
		}
	}
	if pulled == 0 {
		// Fallback: return a tiny silence chunk to avoid stalling and count underrun
		silenceFrames := 128
		if silenceFrames > maxReq {
			silenceFrames = maxReq
		}
		for k := 0; k < silenceFrames*4 && k+3 < len(p); k += 4 {
			binary.LittleEndian.PutUint16(p[k:], 0)
			binary.LittleEndian.PutUint16(p[k+2:], 0)
		}
		s.underruns++
		s.lastWant = silenceFrames
		s.lastPulled = silenceFrames
		return silenceFrames * 4, nil
	}
	s.lastWant = pulled
	s.lastPulled = pulled
	return pulled * 4, nil
}
