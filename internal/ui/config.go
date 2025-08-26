package ui

// Config contains window/input/audio related settings.
type Config struct {
    Title string // window title
    Scale int    // integer upscaling factor
    // Later: fullscreen, vsync toggle, key mapping, etc.
}
