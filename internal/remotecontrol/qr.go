package remotecontrol

import (
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// Render encodes url as a QR code drawn with Unicode half-block characters,
// suitable for scanning off a terminal by a phone camera. Two matrix rows are
// packed into each text line (▀ ▄ █ and space), halving the printed height.
//
// The rendering assumes a dark-background terminal: light QR modules are drawn
// as bright block glyphs and dark modules as the terminal background. go-qrcode
// includes the mandatory quiet-zone border in its bitmap.
//
// On any encoding error Render returns ("", err); callers should degrade
// gracefully by printing the URL alone (the QR is a convenience, not required).
func Render(url string) (string, error) {
	q, err := qrcode.New(url, qrcode.Medium)
	if err != nil {
		return "", err
	}
	bm := q.Bitmap() // true = dark module; includes quiet-zone border.

	var b strings.Builder
	for y := 0; y < len(bm); y += 2 {
		row := bm[y]
		for x := 0; x < len(row); x++ {
			// A "light" pixel is drawn as a block; a "dark" pixel is left as
			// background. Rows beyond the matrix are treated as light (quiet).
			topLight := !bm[y][x]
			botLight := true
			if y+1 < len(bm) {
				botLight = !bm[y+1][x]
			}
			switch {
			case topLight && botLight:
				b.WriteRune('█')
			case topLight && !botLight:
				b.WriteRune('▀')
			case !topLight && botLight:
				b.WriteRune('▄')
			default:
				b.WriteByte(' ')
			}
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}
