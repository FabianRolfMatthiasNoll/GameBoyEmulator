package cart

import (
	"encoding/binary"
	"errors"
	"strings"
)

const (
	headerStart = 0x0100
	headerEnd   = 0x014F
)

var nintendoLogo = [48]byte{
	0xCE,0xED,0x66,0x66,0xCC,0x0D,0x00,0x0B,0x03,0x73,0x00,0x83,0x00,0x0C,0x00,0x0D,
	0x00,0x08,0x11,0x1F,0x88,0x89,0x00,0x0E,0xDC,0xCC,0x6E,0xE6,0xDD,0xDD,0xD9,0x99,
	0xBB,0xBB,0x67,0x63,0x6E,0x0E,0xEC,0xCC,0xDD,0xDC,0x99,0x9F,0xBB,0xB9,0x33,0x3E,
}

type Header struct {
	Title           string // (trimmed ASCII)
	CGBFlag         byte   // 0x0143
	NewLicensee     string // 0x0144-0x0145 (ASCII), if old==0x33
	SGBFlag         byte   // 0x0146
	CartType        byte   // 0x0147
	ROMSizeCode     byte   // 0x0148
	RAMSizeCode     byte   // 0x0149
	Destination     byte   // 0x014A
	OldLicensee     byte   // 0x014B
	ROMVersion      byte   // 0x014C
	HeaderChecksum  byte   // 0x014D
	GlobalChecksum  uint16 // 0x014E-0x014F

	// Decoded helpers (for logs)
	ROMSizeBytes int
	ROMBanks     int
	RAMSizeBytes int
	CartTypeStr  string
}

func ParseHeader(rom []byte) (*Header, error) {
	if len(rom) < headerEnd+1 {
		return nil, errors.New("ROM too small to contain header")
	}

	// Verify Nintendo logo
	for i := 0; i < 48; i++ {
		if rom[0x0104+i] != nintendoLogo[i] {
			// don't fail; just continue (some homebrew/test ROMs might not include it)
			break
		}
	}

	// Title region is 0x0134–0x0143, but parts overlap on newer carts.
	rawTitle := rom[0x0134 : 0x0144]
	title := strings.TrimRight(string(rawTitle), "\x00")

	h := &Header{
		Title:          title,
		CGBFlag:        rom[0x0143],
		NewLicensee:    string(rom[0x0144:0x0146]),
		SGBFlag:        rom[0x0146],
		CartType:       rom[0x0147],
		ROMSizeCode:    rom[0x0148],
		RAMSizeCode:    rom[0x0149],
		Destination:    rom[0x014A],
		OldLicensee:    rom[0x014B],
		ROMVersion:     rom[0x014C],
		HeaderChecksum: rom[0x014D],
		GlobalChecksum: binary.BigEndian.Uint16(rom[0x014E:0x0150]),
	}

	// Decode a few convenience fields:
	h.ROMSizeBytes, h.ROMBanks = decodeROMSize(h.ROMSizeCode)
	h.RAMSizeBytes = decodeRAMSize(h.RAMSizeCode)
	h.CartTypeStr = cartTypeString(h.CartType)

	return h, nil
}

func HeaderChecksumOK(rom []byte) bool {
	if len(rom) < 0x014E {
		return false
	}
	var sum byte = 0
	for addr := 0x0134; addr <= 0x014C; addr++ {
		sum = sum - rom[addr] - 1
	}
	return sum == rom[0x014D]
}

func decodeROMSize(code byte) (size, banks int) {
	switch code {
	case 0x00:
		return 32 * 1024, 2
	case 0x01:
		return 64 * 1024, 4
	case 0x02:
		return 128 * 1024, 8
	case 0x03:
		return 256 * 1024, 16
	case 0x04:
		return 512 * 1024, 32
	case 0x05:
		return 1 * 1024 * 1024, 64
	case 0x06:
		return 2 * 1024 * 1024, 128
	case 0x07:
		return 4 * 1024 * 1024, 256
	case 0x08:
		return 8 * 1024 * 1024, 512
	case 0x52:
		return 1152 * 1024, 72
	case 0x53:
		return 1280 * 1024, 80
	case 0x54:
		return 1536 * 1024, 96
	default:
		return 0, 0
	}
}

func decodeRAMSize(code byte) int {
	switch code {
	case 0x00:
		return 0
	case 0x02:
		return 8 * 1024
	case 0x03:
		return 32 * 1024
	case 0x04:
		return 128 * 1024
	case 0x05:
		return 64 * 1024
	default:
		return 0
	}
}

func cartTypeString(code byte) string {
	switch code {
	case 0x00:
		return "ROM ONLY"
	case 0x01, 0x02, 0x03:
		return "MBC1 (variants)"
	case 0x05, 0x06:
		return "MBC2 (variants)"
	case 0x0F, 0x10, 0x11, 0x12, 0x13:
		return "MBC3 (variants)"
	case 0x19, 0x1A, 0x1B, 0x1C, 0x1D, 0x1E:
		return "MBC5 (variants)"
	default:
		return "Other/unknown"
	}
}
