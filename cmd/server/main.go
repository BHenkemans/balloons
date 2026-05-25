package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BHenkemans/balloons/gen/balloons/v1/balloonsv1connect"
	"github.com/BHenkemans/balloons/internal/config"
	"github.com/BHenkemans/balloons/internal/domjudge"
	"github.com/BHenkemans/balloons/internal/printer"
	"github.com/BHenkemans/balloons/internal/server"
	"github.com/BHenkemans/balloons/internal/state"
)

func main() {
	dj := domjudge.New(
		mustEnv("DOMJUDGE_URL"),
		mustEnv("DOMJUDGE_USER"),
		mustEnv("DOMJUDGE_PASS"),
		mustEnv("DOMJUDGE_CONTEST_ID"),
	)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	p, err := buildPrinter()
	if err != nil {
		log.Fatal(err)
	}

	store, err := state.Open(config.Getenv("STATE_DB", "balloons.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	addr := config.Getenv("ADDR", ":8080")
	scanBaseURL := resolveScanBaseURL(os.Getenv("SCAN_BASE_URL"), addr)
	log.Printf("scan base URL: %s", scanBaseURL)

	loc, err := resolveTZ(os.Getenv("CONTEST_TZ"))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("ticket timezone: %s", loc)

	hub := server.NewHub(dj, p, store,
		config.ParseCSV(os.Getenv("HIDE_GROUP_IDS")),
		config.ParseCSV(os.Getenv("NO_FIRST_SOLVE_GROUP_IDS")),
		scanBaseURL,
		loc,
	)
	go hub.Run(ctx)

	cookieMode := server.ParseCookieSecureMode(os.Getenv("COOKIE_SECURE"))
	svc := &server.Server{Hub: hub, DJ: dj, Store: store, CookieMode: cookieMode}
	path, handler := balloonsv1connect.NewBalloonServiceHandler(svc)

	mux := http.NewServeMux()
	// WithRequestScheme stamps the request context with "http" / "https" so
	// connectRPC handlers can issue Secure cookies only when the inbound
	// request was actually TLS (or proxied with X-Forwarded-Proto: https).
	mux.Handle(path, server.WithRequestScheme(handler))
	mux.HandleFunc("/scan", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "web/scan.html")
	})
	mux.HandleFunc("/runner", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "web/runner.html")
	})
	mux.Handle("/", http.FileServer(http.Dir("web")))

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("listening on %s, mounted %s", addr, path)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// resolveTZ parses an IANA timezone name (e.g. "Europe/Amsterdam") into a
// *time.Location for ticket datetime formatting. Empty string returns
// time.Local — fine on hosts that have their system TZ set correctly, but in
// containers/systemd units where the process inherits UTC, set CONTEST_TZ
// explicitly so ticket timestamps don't drift.
func resolveTZ(name string) (*time.Location, error) {
	if name == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("CONTEST_TZ %q: %w", name, err)
	}
	return loc, nil
}

// resolveScanBaseURL returns the configured SCAN_BASE_URL when set, otherwise
// derives one from os.Hostname() and addr so the QR on every ticket still
// points somewhere reachable on the contest LAN. The derived URL is only
// useful when the printing host is reachable from the runner's phone by
// hostname — set SCAN_BASE_URL explicitly when that's not the case (e.g.
// behind a reverse proxy, or when the host has a public DNS name).
func resolveScanBaseURL(env, addr string) string {
	if env != "" {
		return strings.TrimRight(env, "/")
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "localhost"
	}
	// addr is in `:port` or `host:port` form; we want the port suffix.
	portSuffix := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		portSuffix = addr[i:]
	} else {
		portSuffix = ":" + addr
	}
	return "http://" + host + portSuffix
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("missing env var: %s", k)
	}
	return v
}

func buildPrinter() (printer.Printer, error) {
	kind := config.Getenv("PRINTER_KIND", "noop")
	switch kind {
	case "noop":
		return printer.Noop{}, nil
	case "ipp":
		uri := os.Getenv("PRINTER_IPP_URI")
		if uri == "" {
			return nil, fmt.Errorf("PRINTER_KIND=ipp requires PRINTER_IPP_URI")
		}
		template := config.Getenv("PRINTER_TEMPLATE", "templates/balloon.typ")
		return printer.NewIPP(uri, template)
	case "escpos":
		addr := os.Getenv("PRINTER_ESCPOS_ADDR")
		if addr == "" {
			return nil, fmt.Errorf("PRINTER_KIND=escpos requires PRINTER_ESCPOS_ADDR (host:port)")
		}
		template := config.Getenv("PRINTER_TEMPLATE", "templates/balloon.typ")
		width := 576
		if v := os.Getenv("PRINTER_ESCPOS_WIDTH"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("PRINTER_ESCPOS_WIDTH must be a positive integer, got %q", v)
			}
			width = n
		}
		return printer.NewESCPOS(addr, template, width)
	default:
		return nil, fmt.Errorf("unknown PRINTER_KIND %q (want noop, ipp, or escpos)", kind)
	}
}
