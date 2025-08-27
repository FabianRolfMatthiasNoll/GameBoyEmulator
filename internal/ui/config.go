package ui

// Config contains window/input/audio related settings.
type Config struct {
	Title       string // window title
	Scale       int    // integer upscaling factor
	AudioStereo bool   // if true, output true stereo; if false, fold to mono
	// Audio buffering
	AudioAdaptive bool   // adaptive target on underrun
	AudioBufferMs int    // initial desired buffer in ms (approx)
	ROMsDir       string // directory to browse for ROMs
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
		c.AudioBufferMs = 125
	}
	if c.ROMsDir == "" {
		c.ROMsDir = "roms"
	}
}
