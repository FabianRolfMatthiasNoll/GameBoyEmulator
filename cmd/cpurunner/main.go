package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/bus"
	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/cpu"
)

// writerFunc adapts a function to io.Writer
type writerFunc func(p []byte) (n int, err error)

func (f writerFunc) Write(p []byte) (n int, err error) { return f(p) }

func main() {
	romPath := flag.String("rom", "", "path to ROM (.gb)")
	bootPath := flag.String("bootrom", "", "optional DMG boot ROM to run from 0x0000 until FF50 disables it")
	steps := flag.Int("steps", 5_000_000, "max CPU steps to run")
	startPC := flag.Int("pc", 0x0100, "initial PC value")
	trace := flag.Bool("trace", false, "print PC/opcodes")
	until := flag.String("until", "Passed", "stop when serial output contains this substring (case-insensitive); empty to disable")
	auto := flag.Bool("auto", false, "auto-detect 'Passed' or 'Failed N tests' in serial output and exit with code 0/1")
	timeout := flag.Duration("timeout", 0, "optional wall-clock timeout (e.g. 30s, 2m); 0 disables")
	traceOnFail := flag.Bool("traceOnFail", false, "when -auto detects failure, print a recent trace window (slows down)")
	traceWindow := flag.Int("traceWindow", 200, "number of recent instructions to include in 'traceOnFail' dump")
	serialWindowFlag := flag.Int("serialWindow", 8192, "number of recent serial bytes to retain for diagnostics on fail")
	flag.Parse()

	if *romPath == "" {
		log.Fatal("-rom is required")
	}
	rom, err := os.ReadFile(*romPath)
	if err != nil {
		log.Fatalf("read rom: %v", err)
	}
	var boot []byte
	if *bootPath != "" {
		if b, err := os.ReadFile(*bootPath); err == nil {
			boot = b
		} else {
			log.Fatalf("read bootrom: %v", err)
		}
	}

	b := bus.New(rom)
	if len(boot) >= 0x100 {
		b.SetBootROM(boot)
	}
	// Stream serial to stdout and capture in-memory for pattern detection
	var ser bytes.Buffer
	// Keep a compact serial ring for last N bytes to print on failure
	serialWindow := *serialWindowFlag
	if serialWindow < 256 {
		serialWindow = 256
	}
	serRing := make([]byte, serialWindow)
	serRingIdx := 0
	serRingFill := 0
	w := io.Writer(os.Stdout)
	if *until != "" || *auto {
		w = io.MultiWriter(os.Stdout, &ser)
	}
	// Wrap writer to also update the ring
	if *until != "" || *auto {
		w = io.MultiWriter(os.Stdout, &ser, writerFunc(func(p []byte) (int, error) {
			for _, ch := range p {
				serRing[serRingIdx] = ch
				serRingIdx = (serRingIdx + 1) % serialWindow
				if serRingFill < serialWindow {
					serRingFill++
				}
			}
			return len(p), nil
		}))
	}
	b.SetSerialWriter(w)

	c := cpu.New(b)
	if len(boot) >= 0x100 {
		// Boot ROM path: start at 0x0000; rely on boot to init IO
		c.SP = 0xFFFE
		c.PC = 0x0000
		c.IME = false
	} else {
		// No boot ROM: initialize to DMG post-boot defaults
		c.ResetNoBoot()
		c.SetPC(uint16(*startPC))
		// Minimal DMG post-boot IO defaults (LCD on, palettes, scroll=0, timers off)
		b.Write(0xFF00, 0xCF)
		b.Write(0xFF05, 0x00) // TIMA
		b.Write(0xFF06, 0x00) // TMA
		b.Write(0xFF07, 0x00) // TAC
		b.Write(0xFF40, 0x91) // LCDC on with BG and sprites
		b.Write(0xFF42, 0x00) // SCY
		b.Write(0xFF43, 0x00) // SCX
		b.Write(0xFF45, 0x00) // LYC
		b.Write(0xFF47, 0xFC) // BGP
		b.Write(0xFF48, 0xFF) // OBP0
		b.Write(0xFF49, 0xFF) // OBP1
		b.Write(0xFF4A, 0x00) // WY
		b.Write(0xFF4B, 0x00) // WX
		b.Write(0xFFFF, 0x00) // IE
	}

	start := time.Now()
	var deadline time.Time
	if *timeout > 0 {
		deadline = start.Add(*timeout)
	}
	// Regex for failure summary: "Failed <n> tests"
	failRe := regexp.MustCompile(`(?i)failed\s+(\d+)\s+tests?`)
	// Regex to capture test markers like "11:01"
	stageRe := regexp.MustCompile(`\b(\d{2}:\d{2})\b`)
	lastStage := ""

	// ring buffer for recent traces
	type traceEntry struct {
		pc                     uint16
		op                     byte
		cyc                    int
		a, f, b, c, d, e, h, l byte
		sp                     uint16
		ime                    bool
		ifreg                  byte
		ie                     byte
	}
	ring := make([]traceEntry, *traceWindow)
	ringIdx := 0
	ringFill := 0
	var cycles int
	for i := 0; i < *steps; i++ {
		pc := c.PC
		var op byte
		if *trace || *traceOnFail {
			op = b.Read(pc)
		}
		cyc := c.Step()
		cycles += cyc
		if *trace || *traceOnFail {
			te := traceEntry{
				pc:  pc,
				op:  op,
				cyc: cyc,
				a:   c.A, f: c.F, b: c.B, c: c.C, d: c.D, e: c.E, h: c.H, l: c.L,
				sp: c.SP, ime: c.IME, ifreg: b.Read(0xFF0F), ie: b.Read(0xFFFF),
			}
			if *trace {
				fmt.Printf("PC=%04X OP=%02X cyc=%d A=%02X F=%02X B=%02X C=%02X D=%02X E=%02X H=%02X L=%02X SP=%04X IME=%t IF=%02X IE=%02X\n",
					te.pc, te.op, te.cyc, te.a, te.f, te.b, te.c, te.d, te.e, te.h, te.l, te.sp, te.ime, te.ifreg, te.ie)
			}
			if *traceOnFail && *traceWindow > 0 {
				ring[ringIdx] = te
				ringIdx = (ringIdx + 1) % *traceWindow
				if ringFill < *traceWindow {
					ringFill++
				}
			}
		}
		if *auto {
			s := ser.String()
			if mm := stageRe.FindAllString(s, -1); len(mm) > 0 {
				lastStage = mm[len(mm)-1]
			}
			if strings.Contains(strings.ToLower(s), "passed") {
				fmt.Printf("\nDetected PASS in serial output.\n")
				if lastStage != "" {
					fmt.Printf("Last stage seen: %s\n", lastStage)
				}
				fmt.Printf("\nDone: steps=%d cycles~=%d elapsed=%s\n", i+1, cycles, time.Since(start).Truncate(time.Millisecond))
				os.Exit(0)
			}
			if m := failRe.FindStringSubmatch(s); m != nil {
				fmt.Printf("\nDetected %s in serial output.\n", m[0])
				if lastStage != "" {
					fmt.Printf("Last stage seen: %s\n", lastStage)
				}
				if *traceOnFail && ringFill > 0 {
					fmt.Printf("\n--- recent trace (last %d instructions) ---\n", ringFill)
					// print in chronological order
					startIdx := (ringIdx - ringFill + *traceWindow) % *traceWindow
					for j := 0; j < ringFill; j++ {
						idx := (startIdx + j) % *traceWindow
						te := ring[idx]
						fmt.Printf("PC=%04X OP=%02X cyc=%d A=%02X F=%02X B=%02X C=%02X D=%02X E=%02X H=%02X L=%02X SP=%04X IME=%t IF=%02X IE=%02X\n",
							te.pc, te.op, te.cyc, te.a, te.f, te.b, te.c, te.d, te.e, te.h, te.l, te.sp, te.ime, te.ifreg, te.ie)
					}
					fmt.Printf("--- end trace ---\n")
				}
				if serRingFill > 0 {
					fmt.Printf("\n--- recent serial (last %d bytes) ---\n", serRingFill)
					start := (serRingIdx - serRingFill + serialWindow) % serialWindow
					for j := 0; j < serRingFill; j++ {
						idx := (start + j) % serialWindow
						fmt.Printf("%c", serRing[idx])
					}
					fmt.Printf("\n--- end serial ---\n")
				}
				fmt.Printf("\nDone: steps=%d cycles~=%d elapsed=%s\n", i+1, cycles, time.Since(start).Truncate(time.Millisecond))
				os.Exit(1)
			}
		} else if *until != "" {
			if strings.Contains(strings.ToLower(ser.String()), strings.ToLower(*until)) {
				fmt.Printf("\nDetected '%s' in serial output.\n", *until)
				fmt.Printf("\nDone: steps=%d cycles~=%d elapsed=%s\n", i+1, cycles, time.Since(start).Truncate(time.Millisecond))
				return
			}
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			fmt.Printf("\nTimeout after %s.\n", time.Since(start).Truncate(time.Millisecond))
			fmt.Printf("\nDone: steps=%d cycles~=%d elapsed=%s\n", i+1, cycles, time.Since(start).Truncate(time.Millisecond))
			os.Exit(2)
		}
	}
	dur := time.Since(start)
	fmt.Printf("\nDone: steps=%d cycles~=%d elapsed=%s\n", *steps, cycles, dur.Truncate(time.Millisecond))
}
