package printer

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/png"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ESCPOS renders the ticket as a PNG via Typst and pushes it to a thermal
// printer as ESC/POS raster data over a TCP raw socket (typically port 9100).
// The Typst template is rendered at a PPI chosen so an 80mm page lands at
// exactly the configured dot width.
type ESCPOS struct {
	addr     string // host:port for TCP raw printing
	template string
	width    int // target raster width in dots
}

// pageWidthMM matches the Typst template's `#let page-width = 80mm`. Changing
// it here without updating the template will misalign the rendered PNG.
const pageWidthMM = 80.0

// NewESCPOS validates the address and returns a printer. width is the printer
// head width in dots; 576 fits the common 80mm/203dpi thermal printer.
func NewESCPOS(addr, template string, width int) (*ESCPOS, error) {
	if addr == "" {
		return nil, fmt.Errorf("printer: ESC/POS requires PRINTER_ESCPOS_ADDR (host:port)")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return nil, fmt.Errorf("printer: invalid ESC/POS address %q: %w", addr, err)
	}
	if width <= 0 {
		width = 576
	}
	return &ESCPOS{addr: addr, template: template, width: width}, nil
}

func (p *ESCPOS) Print(ctx context.Context, t Ticket) error {
	pngPath, err := p.render(ctx, t)
	if err != nil {
		return err
	}
	defer os.Remove(pngPath)

	img, err := loadImage(pngPath)
	if err != nil {
		return fmt.Errorf("printer: load rendered PNG: %w", err)
	}

	payload := encodeESCPOS(img, p.width)

	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", p.addr)
	if err != nil {
		return fmt.Errorf("printer: ESC/POS dial %s: %w", p.addr, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(dl)
	} else {
		_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	}
	if _, err := conn.Write(payload); err != nil {
		return fmt.Errorf("printer: ESC/POS write: %w", err)
	}
	return nil
}

// render compiles the Typst template to a PNG sized to the printer width.
// PPI is chosen so the 80mm page renders to exactly `width` pixels.
func (p *ESCPOS) render(ctx context.Context, t Ticket) (string, error) {
	out := filepath.Join(os.TempDir(), fmt.Sprintf("balloon-%d-%d.png", t.BalloonID, time.Now().UnixNano()))
	issued := t.IssuedAt
	if issued.IsZero() {
		issued = time.Now()
	}
	ppi := float64(p.width) * 25.4 / pageWidthMM
	cmd := exec.CommandContext(ctx, "typst", "compile",
		"--format", "png",
		"--ppi", strconv.FormatFloat(ppi, 'f', 3, 64),
		"--input", "datetime="+issued.Format("02-01-2006 15:04"),
		"--input", "ticket_id="+strconv.FormatInt(t.BalloonID, 10),
		"--input", "problem="+t.ProblemLabel,
		"--input", "color="+t.ProblemRGB,
		"--input", "team_name="+t.TeamName,
		"--input", "team_id="+t.TeamID,
		"--input", "balloons="+strings.Join(t.AllProblems, ","),
		"--input", "delivered="+strings.Join(t.Delivered, ","),
		"--input", "in_delivery="+strings.Join(t.InDelivery, ","),
		"--input", "first_solve="+strconv.FormatBool(t.FirstSolve),
		p.template, out,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(out)
		return "", fmt.Errorf("printer: typst compile: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func loadImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

// encodeESCPOS builds the byte stream sent to the printer:
// init, then the image as a GS v 0 raster bitmap split into row chunks (some
// printers reject very tall single chunks), then feed + partial cut.
func encodeESCPOS(img image.Image, targetWidth int) []byte {
	bw, w, h := imageTo1Bit(img, targetWidth)

	var buf bytes.Buffer
	buf.Write([]byte{0x1b, 0x40}) // ESC @ — initialize

	rowBytes := (w + 7) / 8
	const chunkRows = 128 // GS v 0 is reliable in small chunks across firmwares
	for y0 := 0; y0 < h; y0 += chunkRows {
		rows := chunkRows
		if y0+rows > h {
			rows = h - y0
		}
		// GS v 0 m xL xH yL yH — m=0 is normal (non-doubled) raster
		buf.Write([]byte{
			0x1d, 0x76, 0x30, 0x00,
			byte(rowBytes & 0xff), byte(rowBytes >> 8),
			byte(rows & 0xff), byte(rows >> 8),
		})
		for ry := 0; ry < rows; ry++ {
			rowStart := (y0 + ry) * w
			for xb := 0; xb < rowBytes; xb++ {
				var b byte
				base := xb * 8
				for bit := 0; bit < 8; bit++ {
					x := base + bit
					if x < w && bw[rowStart+x] {
						b |= 1 << (7 - bit)
					}
				}
				buf.WriteByte(b)
			}
		}
	}

	// Feed past the cutter and partial-cut. GS V B n feeds n dots then cuts.
	buf.Write([]byte{0x1d, 0x56, 0x42, 0x40}) // feed 64 dots, partial cut
	return buf.Bytes()
}

// imageTo1Bit converts img to a 1-bit raster of width targetWidth (cropping
// or right-padding with white as needed) using Floyd-Steinberg dithering on
// the luminance channel. true = black (printed).
func imageTo1Bit(img image.Image, targetWidth int) (pixels []bool, width, height int) {
	src := img.Bounds()
	srcW, srcH := src.Dx(), src.Dy()
	width = targetWidth
	height = srcH
	if width <= 0 {
		width = srcW
	}

	// Luminance buffer in [0,1]; outside the source image we leave white (1).
	lum := make([]float64, width*height)
	for y := 0; y < height; y++ {
		row := y * width
		for x := 0; x < width; x++ {
			if x < srcW {
				r, g, b, a := img.At(src.Min.X+x, src.Min.Y+y).RGBA()
				// Composite against white so transparent PNG areas don't print.
				af := float64(a) / 65535.0
				rf := float64(r)/65535.0*af + (1 - af)
				gf := float64(g)/65535.0*af + (1 - af)
				bf := float64(b)/65535.0*af + (1 - af)
				lum[row+x] = 0.299*rf + 0.587*gf + 0.114*bf
			} else {
				lum[row+x] = 1
			}
		}
	}

	pixels = make([]bool, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			i := y*width + x
			old := lum[i]
			var newVal float64
			if old < 0.5 {
				newVal = 0
				pixels[i] = true
			} else {
				newVal = 1
			}
			err := old - newVal
			if x+1 < width {
				lum[i+1] += err * 7 / 16
			}
			if y+1 < height {
				if x > 0 {
					lum[i+width-1] += err * 3 / 16
				}
				lum[i+width] += err * 5 / 16
				if x+1 < width {
					lum[i+width+1] += err * 1 / 16
				}
			}
		}
	}
	return pixels, width, height
}
