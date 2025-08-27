package ui

// Config contains window/input/audio related settings.
type Config struct {
	Title       string // window title
	Scale       int    // integer upscaling factor
	AudioStereo bool   // if true, output true stereo; if false, fold to mono
	// Audio buffering
	AudioAdaptive bool // adaptive target on underrun
	AudioBufferMs int  // initial desired buffer in ms (approx)
	// Later: fullscreen, vsync toggle, key mapping, etc.
}
