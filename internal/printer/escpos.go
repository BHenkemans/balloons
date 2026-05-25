package printer

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/png"
	"math"
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
//
// The render pipeline supersamples the Typst output (so each printer dot is
// the area-average of supersample² source pixels) and converts the result to
// 1-bit with a chroma-aware ink density: saturated colors become solid black
// ink instead of dithering to noise, while grayscale photo regions go through
// Floyd-Steinberg as usual. This avoids the speckled-blob look that simple
// dithering produces on colored fills.
type ESCPOS struct {
	addr     string // host:port for TCP raw printing
	template string
	width    int // target raster width in dots
}

// Thermal printers in this family are 203 DPI. The Typst page width is
// derived as width / DPI so each rendered pixel maps cleanly to a printer
// dot at supersample=1.
const targetDPI = 203.0

// supersample is how many source pixels per output dot, per axis. 2 captures
// Typst's anti-aliasing without quadrupling rasterization cost; 3+ shows
// diminishing returns on a 203 DPI printer.
const supersample = 2

// chromaThreshold is the maximum sRGB chroma (max channel - min channel)
// that a pixel may have before it's treated as "colored ink" and forced to
// solid black rather than dithered. 0.15 keeps faint anti-aliased edges in
// the photo path but catches the brand colors used in the template.
const chromaThreshold = 0.15

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

// render compiles the Typst template to a PNG that is supersample× wider
// than the printer's dot count. The page width (in mm) is derived from the
// dot width / targetDPI so the rendered pixel grid lines up with printer
// dots after downsampling.
func (p *ESCPOS) render(ctx context.Context, t Ticket) (string, error) {
	out := filepath.Join(os.TempDir(), fmt.Sprintf("balloon-%d-%d.png", t.BalloonID, time.Now().UnixNano()))
	pageWidthMM := float64(p.width) * 25.4 / targetDPI
	ppi := targetDPI * float64(supersample)
	args := []string{
		"compile",
		"--format", "png",
		"--ppi", strconv.FormatFloat(ppi, 'f', 3, 64),
	}
	args = append(args, typstInputs(t)...)
	args = append(args,
		"--input", "theme=thermal",
		"--input", "page_width_mm="+strconv.FormatFloat(pageWidthMM, 'f', 3, 64),
		p.template, out,
	)
	cmd := exec.CommandContext(ctx, "typst", args...)
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
	buf.Write([]byte{0x1b, 0x40})       // ESC @ — initialize
	buf.Write([]byte{0x1b, 0x33, 0x00}) // ESC 3 0 — line spacing to 0 so chunked GS v 0 rasters butt against each other

	rowBytes := (w + 7) / 8
	// chunkRows is kept just under the most-conservative firmware limit
	// (2047 rows per GS v 0 on some older Epson clones). Bigger is better:
	// many printers add a small paper advance between consecutive GS v 0
	// raster commands regardless of `ESC 3 0`, so every chunk boundary is a
	// potential visible gap. 1024 is a good balance — for a typical
	// 150–250mm receipt that's 1–2 chunks instead of 10+.
	const chunkRows = 1024
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
		for ry := range rows {
			rowStart := (y0 + ry) * w
			for xb := range rowBytes {
				var b byte
				base := xb * 8
				for bit := range 8 {
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

// imageTo1Bit converts img to a 1-bit raster of width targetWidth using a
// three-stage pipeline: area-filter downsample (the supersample step), per
// pixel chroma-aware ink density, then Floyd-Steinberg dither on the density
// field. true = black (printed).
//
// The downsample averages in linear-light space so anti-aliased edges and
// gradients don't get the gamma-darkening artefact of averaging sRGB values
// directly. Chroma detection keeps the dither pattern off solid colored
// fills (problem letter, first-solve banner) by snapping them to either
// solid black or — for very light colors like pale yellow — back to the
// luminance path.
func imageTo1Bit(img image.Image, targetWidth int) (pixels []bool, width, height int) {
	width, height = targetDimensions(img.Bounds(), targetWidth)
	density := computeDensity(img, width, height)
	pixels = floydSteinberg(density, width, height)
	return pixels, width, height
}

// targetDimensions returns the output (width, height) that preserves the
// source aspect ratio while honoring the requested width. height is at least 1.
func targetDimensions(src image.Rectangle, targetWidth int) (width, height int) {
	srcW, srcH := src.Dx(), src.Dy()
	width = targetWidth
	if width <= 0 || width > srcW {
		width = srcW
	}
	scale := float64(srcW) / float64(width)
	height = max(int(math.Round(float64(srcH)/scale)), 1)
	return width, height
}

// computeDensity area-averages img into a width×height ink-density field
// (0=white, 1=black) using chroma-aware compositing — saturated colors snap to
// solid black instead of dithering to a noisy mid-tone.
func computeDensity(img image.Image, width, height int) []float64 {
	src := img.Bounds()
	srcW, srcH := src.Dx(), src.Dy()
	scale := float64(srcW) / float64(width)
	density := make([]float64, width*height)
	for oy := range height {
		sy0, sy1 := sampleSpan(oy, scale, srcH)
		for ox := range width {
			sx0, sx1 := sampleSpan(ox, scale, srcW)
			density[oy*width+ox] = inkAt(img, src, sx0, sx1, sy0, sy1)
		}
	}
	return density
}

// sampleSpan maps an output coordinate to the [lo, hi) source-pixel range it
// area-averages over. Clamps hi to srcMax and guarantees hi > lo.
func sampleSpan(outCoord int, scale float64, srcMax int) (lo, hi int) {
	lo = int(float64(outCoord) * scale)
	hi = min(int(float64(outCoord+1)*scale), srcMax)
	if hi <= lo {
		hi = lo + 1
	}
	return lo, hi
}

// inkAt computes the ink density of one output pixel by averaging the source
// region in linear-light sRGB. Saturated mid-or-darker colors are forced to
// solid ink; pale or near-grayscale pixels fall through to luminance.
func inkAt(img image.Image, src image.Rectangle, sx0, sx1, sy0, sy1 int) float64 {
	var rLin, gLin, bLin, count float64
	for y := sy0; y < sy1; y++ {
		for x := sx0; x < sx1; x++ {
			r, g, b, a := img.At(src.Min.X+x, src.Min.Y+y).RGBA()
			af := float64(a) / 65535.0
			// Composite over white so transparent regions print white.
			sr := float64(r)/65535.0*af + (1 - af)
			sg := float64(g)/65535.0*af + (1 - af)
			sb := float64(b)/65535.0*af + (1 - af)
			rLin += srgbToLinear(sr)
			gLin += srgbToLinear(sg)
			bLin += srgbToLinear(sb)
			count++
		}
	}
	rLin /= count
	gLin /= count
	bLin /= count

	// Chroma is perceptual — compute it in display space.
	rs := linearToSrgb(rLin)
	gs := linearToSrgb(gLin)
	bs := linearToSrgb(bLin)
	chroma := math.Max(rs, math.Max(gs, bs)) - math.Min(rs, math.Min(gs, bs))

	lumLinear := 0.2126*rLin + 0.7152*gLin + 0.0722*bLin
	perceived := linearToSrgb(lumLinear)

	if chroma > chromaThreshold && perceived < 0.7 {
		return 1
	}
	return 1 - perceived
}

// floydSteinberg dithers density (modified in-place as the error diffuses) to
// a 1-bit raster of the same dimensions. true = black.
func floydSteinberg(density []float64, width, height int) []bool {
	pixels := make([]bool, width*height)
	for y := range height {
		for x := range width {
			i := y*width + x
			old := density[i]
			var newVal float64
			if old > 0.5 {
				newVal = 1
				pixels[i] = true
			}
			diffuseError(density, x, y, width, height, old-newVal)
		}
	}
	return pixels
}

// diffuseError spreads the per-pixel quantization error to the standard
// Floyd-Steinberg neighbours (7/16 right, 3/16 below-left, 5/16 below,
// 1/16 below-right), clipped at the image boundary.
func diffuseError(density []float64, x, y, width, height int, err float64) {
	i := y*width + x
	if x+1 < width {
		density[i+1] += err * 7 / 16
	}
	if y+1 >= height {
		return
	}
	if x > 0 {
		density[i+width-1] += err * 3 / 16
	}
	density[i+width] += err * 5 / 16
	if x+1 < width {
		density[i+width+1] += err * 1 / 16
	}
}

func srgbToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func linearToSrgb(c float64) float64 {
	if c <= 0.0031308 {
		return c * 12.92
	}
	return 1.055*math.Pow(c, 1/2.4) - 0.055
}
