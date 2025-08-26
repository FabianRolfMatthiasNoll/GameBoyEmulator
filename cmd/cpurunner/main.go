package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/bus"
	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/cpu"
)

func main() {
	romPath := flag.String("rom", "", "path to ROM (.gb)")
	steps := flag.Int("steps", 5_000_000, "max CPU steps to run")
	startPC := flag.Int("pc", 0x0100, "initial PC value")
	trace := flag.Bool("trace", false, "print PC/opcodes")
	flag.Parse()

	if *romPath == "" {
		log.Fatal("-rom is required")
	}
	rom, err := os.ReadFile(*romPath)
	if err != nil {
		log.Fatalf("read rom: %v", err)
	}

	b := bus.New(rom)
	// Stream serial to stdout
	b.SetSerialWriter(os.Stdout)

	c := cpu.New(b)
	c.SetPC(uint16(*startPC))

	start := time.Now()
	var cycles int
	for i := 0; i < *steps; i++ {
		pc := c.PC
		op := b.Read(pc)
		cyc := c.Step()
		cycles += cyc
		if *trace {
			fmt.Printf("PC=%04X OP=%02X cyc=%d\n", pc, op, cyc)
		}
	}
	dur := time.Since(start)
	fmt.Printf("\nDone: steps=%d cycles~=%d elapsed=%s\n", *steps, cycles, dur.Truncate(time.Millisecond))
}
