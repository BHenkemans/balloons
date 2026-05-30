package printer

import (
	"fmt"
	"os"
	"strconv"
)

// FromEnv builds a Printer from PRINTER_* environment variables:
//
//   - PRINTER_KIND ∈ {escpos (default), ipp}
//   - PRINTER_TEMPLATE (default templates/balloon.typ; used by ipp + escpos)
//   - PRINTER_IPP_URI (required for ipp)
//   - PRINTER_ESCPOS_ADDR (required for escpos)
//   - PRINTER_ESCPOS_WIDTH (default 576; printer head width in dots)
func FromEnv() (Printer, error) {
	kind := getenv("PRINTER_KIND", "escpos")
	switch kind {
	case "ipp":
		uri := os.Getenv("PRINTER_IPP_URI")
		if uri == "" {
			return nil, fmt.Errorf("PRINTER_KIND=ipp requires PRINTER_IPP_URI")
		}
		return NewIPP(uri, getenv("PRINTER_TEMPLATE", "templates/balloon.typ"))
	case "escpos":
		addr := os.Getenv("PRINTER_ESCPOS_ADDR")
		if addr == "" {
			return nil, fmt.Errorf("PRINTER_KIND=escpos requires PRINTER_ESCPOS_ADDR (host:port)")
		}
		width := 576
		if v := os.Getenv("PRINTER_ESCPOS_WIDTH"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("PRINTER_ESCPOS_WIDTH must be a positive integer, got %q", v)
			}
			width = n
		}
		return NewESCPOS(addr, getenv("PRINTER_TEMPLATE", "templates/balloon.typ"), width)
	default:
		return nil, fmt.Errorf("unknown PRINTER_KIND %q (want ipp or escpos)", kind)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
