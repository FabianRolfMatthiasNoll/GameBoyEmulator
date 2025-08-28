package ui

// Config contains window/input/audio related settings.
type Config struct {
	Title       string // window title
	Scale       int    // integer upscaling factor
	AudioStereo bool   // if true, output true stereo; if false, fold to mono
	// Audio buffering
	AudioAdaptive   bool   // adaptive target on underrun
	AudioBufferMs   int    // initial desired buffer in ms (approx)
	AudioLowLatency bool   // hard-cap buffering for minimal latency
	ROMsDir         string // directory to browse for ROMs
	UseFetcherBG    bool   // render BG via fetcher/FIFO
	// Visual overlay skin
	ShellOverlay bool   // draw an alpha-blended overlay image over the game view
	ShellImage   string // path to the overlay image (PNG)
	// Per-ROM preferences
	PerROMCompatPalette map[string]int // map of ROM path -> compat palette ID
	// Later: fullscreen, vsync toggle, key mapping, etc.
}

// Defaults fills missing fields with reasonable defaults.
func (c *Config) Defaults() {
	if c.Title == "" {
		c.Title = "gbemu"
	}
	if c.Scale <= 0 {
		c.Scale = 3
	}
	if c.AudioBufferMs <= 0 {
		c.AudioBufferMs = 60 // lower baseline to reduce perceived latency
	}
	if c.ROMsDir == "" {
		c.ROMsDir = "roms"
	}
	if c.PerROMCompatPalette == nil {
		c.PerROMCompatPalette = make(map[string]int)
	}
	// Default overlay path, disabled by default
	if c.ShellImage == "" {
		c.ShellImage = "assets/skins/gbc_overlay.png"
	}
}
