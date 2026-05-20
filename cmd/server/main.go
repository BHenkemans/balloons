package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BHenkemans/balloons/gen/balloons/v1/balloonsv1connect"
	"github.com/BHenkemans/balloons/internal/domjudge"
	"github.com/BHenkemans/balloons/internal/server"
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

	hub := server.NewHub(dj)
	go hub.Run(ctx)

	svc := &server.Server{Hub: hub, DJ: dj}
	path, handler := balloonsv1connect.NewBalloonServiceHandler(svc)

	mux := http.NewServeMux()
	mux.Handle(path, handler)
	mux.Handle("/", http.FileServer(http.Dir("web")))

	addr := getenv("ADDR", ":8080")
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

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
