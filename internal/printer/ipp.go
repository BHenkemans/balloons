package printer

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phin1x/go-ipp"
)

// IPP renders a ticket via Typst and submits it as a print job to an IPP
// endpoint. Unauthenticated; auth can be added when needed.
type IPP struct {
	host     string
	port     int
	useTLS   bool
	queue    string
	template string
}

// NewIPP parses an `ipp://host[:port]/queue` URI and returns a printer.
func NewIPP(uri, template string) (*IPP, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("printer: invalid IPP URI %q: %w", uri, err)
	}
	if u.Scheme != "ipp" && u.Scheme != "ipps" {
		return nil, fmt.Errorf("printer: IPP URI must use ipp:// or ipps://, got %q", u.Scheme)
	}
	port := 631
	if p := u.Port(); p != "" {
		port, err = strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("printer: invalid IPP port: %w", err)
		}
	}
	// go-ipp internally builds `ipp://localhost/printers/<arg>`, so we must
	// hand it just the queue name (the leaf), not the full URI path.
	// `ipp://host:632/printers/SinkPrinter` → queue = "SinkPrinter".
	queue := path.Base(strings.TrimPrefix(u.Path, "/"))
	if queue == "" || queue == "." {
		return nil, fmt.Errorf("printer: IPP URI is missing the queue name (e.g. /printers/SinkPrinter)")
	}
	return &IPP{
		host:     u.Hostname(),
		port:     port,
		useTLS:   u.Scheme == "ipps",
		queue:    queue,
		template: template,
	}, nil
}

func (p *IPP) Print(ctx context.Context, t Ticket) error {
	pdf, err := p.render(ctx, t)
	if err != nil {
		return err
	}
	defer os.Remove(pdf)

	client := ipp.NewIPPClient(p.host, p.port, "", "", p.useTLS)
	// go-ipp v1.7.0 PrintFile writes to jobAttributes unconditionally,
	// so it must be a non-nil map even when we have nothing to set.
	if _, err := client.PrintFile(pdf, p.queue, map[string]any{}); err != nil {
		return fmt.Errorf("printer: IPP submit: %w", err)
	}
	return nil
}

func (p *IPP) render(ctx context.Context, t Ticket) (string, error) {
	out := filepath.Join(os.TempDir(), fmt.Sprintf("balloon-%d-%d.pdf", t.BalloonID, time.Now().UnixNano()))
	args := []string{"compile"}
	args = append(args, typstInputs(t)...)
	args = append(args, "--input", "theme=color", p.template, out)
	cmd := exec.CommandContext(ctx, "typst", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(out)
		return "", fmt.Errorf("printer: typst compile: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
