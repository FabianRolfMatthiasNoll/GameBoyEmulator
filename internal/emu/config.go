package emu

// Config contains settings that affect emulation behavior.
type Config struct {
    Trace    bool  // log CPU instructions
    LimitFPS bool  // throttle to ~60 Hz (useful for headless test mode)
    // Later: fast-forward, GBC enable, debugger flags, etc.
}
