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
	if u := os.Getenv("DOMJUDGE_EVENTFEED_URL"); u != "" {
		log.Printf("event-feed override: %s", u)
		dj.SetEventFeedURL(u)
	}

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

	hub := server.NewHub(dj, p, store,
		config.ParseCSV(os.Getenv("HIDE_GROUP_IDS")),
		config.ParseCSV(os.Getenv("NO_FIRST_SOLVE_GROUP_IDS")),
	)
	go hub.Run(ctx)

	svc := &server.Server{Hub: hub, DJ: dj, Store: store}
	path, handler := balloonsv1connect.NewBalloonServiceHandler(svc)

	mux := http.NewServeMux()
	mux.Handle(path, handler)
	mux.Handle("/", http.FileServer(http.Dir("web")))

	addr := config.Getenv("ADDR", ":8080")
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
