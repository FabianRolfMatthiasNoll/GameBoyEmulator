package emu

// Config contains settings that affect emulation behavior.
type Config struct {
	Trace        bool // log CPU instructions
	LimitFPS     bool // throttle to ~60 Hz (useful for headless test mode)
	UseFetcherBG bool // render BG via fetcher/FIFO scanline path
	UseCGBBG     bool // experimental: use CGB BG/window path with attributes
	// Later: fast-forward, GBC enable, debugger flags, etc.
}
