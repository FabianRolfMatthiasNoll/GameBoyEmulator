package main

import (
	"flag"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/cart"
	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/emu"
	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/ui"
)

type CLIFlags struct {
	ROMPath string
	BootROM string
	Scale   int
	Title   string
	Trace   bool
	SaveRAM bool // persist battery RAM next to ROM (.sav)

	// headless
	Headless bool
	Frames   int
	PNGOut   string
	Expect   string // expected framebuffer CRC32 hex (e.g., "1a2b3c4d")
}

func parseFlags() CLIFlags {
	var f CLIFlags
	flag.StringVar(&f.ROMPath, "rom", "", "path to ROM (.gb)")
	flag.StringVar(&f.BootROM, "bootrom", "", "optional DMG boot ROM")
	flag.IntVar(&f.Scale, "scale", 3, "window scale")
	flag.StringVar(&f.Title, "title", "gbemu", "window title")
	flag.BoolVar(&f.Trace, "trace", false, "CPU trace log")
	flag.BoolVar(&f.SaveRAM, "save", true, "persist battery RAM to ROM.sav on exit and load on start")

	// headless options
	flag.BoolVar(&f.Headless, "headless", false, "run without a window")
	flag.IntVar(&f.Frames, "frames", 300, "frames to run in headless mode")
	flag.StringVar(&f.PNGOut, "outpng", "", "write last framebuffer to PNG at path")
	flag.StringVar(&f.Expect, "expect", "", "assert framebuffer CRC32 (hex)")
	flag.Parse()
	return f
}

func runHeadless(m *emu.Machine, frames int, pngPath, expectCRC string) error {
	if frames <= 0 {
		frames = 1
	}

	start := time.Now()
	for i := 0; i < frames; i++ {
		m.StepFrame()
	}
	dur := time.Since(start)

	fb := m.Framebuffer() // RGBA 160x144*4
	crc := crc32.ChecksumIEEE(fb)
	fps := float64(frames) / dur.Seconds()

	log.Printf("headless: frames=%d elapsed=%s fps=%.2f fb_crc32=%08x",
		frames, dur.Truncate(time.Millisecond), fps, crc)

	if pngPath != "" {
		if err := saveFramePNG(fb, 160, 144, pngPath); err != nil {
			return fmt.Errorf("write PNG: %w", err)
		}
		log.Printf("wrote %s", pngPath)
	}

	if expectCRC != "" {
		// normalize expected hex (allow with/without 0x, upper/lowercase)
		want := strings.TrimPrefix(strings.ToLower(expectCRC), "0x")
		got := fmt.Sprintf("%08x", crc)
		if got != want {
			return fmt.Errorf("checksum mismatch: got %s, want %s", got, want)
		}
	}
	return nil
}

func saveFramePNG(pix []byte, w, h int, path string) error {
	img := &image.RGBA{
		Pix:    make([]byte, len(pix)),
		Stride: 4 * w,
		Rect:   image.Rect(0, 0, w, h),
	}
	copy(img.Pix, pix)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func mustRead(path string) []byte {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("read %s: %v", path, err)
	}
	return b
}

func main() {
	f := parseFlags()
	var rom []byte
	if f.ROMPath != "" {
		rom = mustRead(f.ROMPath)
	}
	boot := mustRead(f.BootROM)

	if len(rom) >= 0x150 {
		if h, err := cart.ParseHeader(rom); err == nil {
			log.Printf("ROM: %q type=%s banks=%d ram=%dB", h.Title, h.CartTypeStr, h.ROMBanks, h.RAMSizeBytes)
		}
	}

	emuCfg := emu.Config{
		Trace:    f.Trace,
		LimitFPS: false, // headless wants max speed
	}
	m := emu.New(emuCfg)
	if len(boot) >= 0x100 {
		m.SetBootROM(boot)
	}
	if len(rom) > 0 {
		if err := m.LoadCartridge(rom, boot); err != nil {
			log.Fatalf("load cart: %v", err)
		}
		// Mark ROM path on the machine so UI knows a game is loaded
		if f.ROMPath != "" {
			// prefer absolute path for state/save placement consistency
			if abs, err := filepath.Abs(f.ROMPath); err == nil {
				_ = m.LoadROMFromFile(abs) // reload through file path to set romPath
			} else {
				_ = m.LoadROMFromFile(f.ROMPath)
			}
		}
	}

	// Battery RAM: load .sav if present
	var savPath string
	if f.SaveRAM && f.ROMPath != "" {
		savPath = strings.TrimSuffix(f.ROMPath, ".gb") + ".sav"
		if data, err := os.ReadFile(savPath); err == nil {
			if m.LoadBattery(data) {
				log.Printf("loaded save RAM: %s (%d bytes)", savPath, len(data))
			}
		}
	}

	if f.Headless {
		if err := runHeadless(m, f.Frames, f.PNGOut, f.Expect); err != nil {
			log.Fatal(err)
		}
		if f.SaveRAM && savPath != "" {
			if data, ok := m.SaveBattery(); ok {
				if err := os.WriteFile(savPath, data, 0644); err == nil {
					log.Printf("wrote %s", savPath)
				}
			}
		}
		return
	}

	uiCfg := ui.Config{Title: f.Title, Scale: f.Scale}
	app := ui.NewApp(uiCfg, m)
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
	// Persist settings after UI exit
	// Best-effort: ignore errors
	if s, ok := any(app).(interface{ SaveSettings() }); ok {
		s.SaveSettings()
	}
	// UI exit: save battery RAM if enabled (derive path from current ROM if needed)
	if f.SaveRAM {
		outSav := savPath
		if outSav == "" && m.ROMPath() != "" && strings.HasSuffix(strings.ToLower(m.ROMPath()), ".gb") {
			outSav = strings.TrimSuffix(m.ROMPath(), ".gb") + ".sav"
		}
		if outSav != "" {
			if data, ok := m.SaveBattery(); ok {
				if err := os.WriteFile(outSav, data, 0644); err == nil {
					log.Printf("wrote %s", outSav)
				}
			}
		}
	}
}
