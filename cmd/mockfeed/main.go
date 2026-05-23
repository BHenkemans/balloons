// mockfeed is a tiny stand-in for DOMjudge's /api/v4/contests/{cid}/event-feed
// stream. It does NOT mock the rest of DOMjudge's API — point the balloon
// server's DOMJUDGE_URL at the real DOMjudge and only override the event-feed
// via DOMJUDGE_EVENTFEED_URL=http://localhost:8090.
//
// Use it like this during dev:
//
//	terminal 1: just mockfeed
//	terminal 2: set DOMJUDGE_EVENTFEED_URL=http://localhost:8090 in .env, just run
//	terminal 3: after inserting a balloon row via SQL, `just trigger`
//
// The mock holds streaming connections open, ignores basic auth, and on every
// POST /trigger broadcasts a DOMjudge-shaped NDJSON event line to all connected
// clients. The balloon hub treats any event line as "something changed, refetch
// from DOMjudge and diff", so the event payload only needs to be well-formed.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type subscriber struct {
	ch chan []byte
}

type hub struct {
	mu     sync.Mutex
	subs   map[*subscriber]struct{}
	nextID atomic.Int64
}

func newHub() *hub {
	return &hub{subs: map[*subscriber]struct{}{}}
}

func (h *hub) subscribe() (*subscriber, func()) {
	s := &subscriber{ch: make(chan []byte, 16)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s, func() {
		h.mu.Lock()
		if _, ok := h.subs[s]; ok {
			delete(h.subs, s)
			close(s.ch)
		}
		h.mu.Unlock()
	}
}

func (h *hub) broadcast(line []byte) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	delivered := 0
	for s := range h.subs {
		select {
		case s.ch <- line:
			delivered++
		default:
			// drop on slow subscribers; real DOMjudge does not pace itself either.
		}
	}
	return delivered
}

// event matches the shape of a real DOMjudge event-feed line closely enough
// that the balloon server's NDJSON scanner is exercised the same way.
type event struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Op   string `json:"op"`
	Time string `json:"time"`
	Data any    `json:"data,omitempty"`
}

func (h *hub) newEvent(t string) []byte {
	id := h.nextID.Add(1)
	e := event{
		Type: t,
		ID:   fmt.Sprintf("mock-%d", id),
		Op:   "create",
		Time: time.Now().UTC().Format("2006-01-02T15:04:05.000-07:00"),
		Data: map[string]any{},
	}
	// All fields are JSON-safe primitives plus a string-keyed map, so Marshal
	// cannot fail here — panic if our assumption is wrong rather than emit a
	// malformed line that would silently break subscribers.
	b, err := json.Marshal(e)
	if err != nil {
		panic(fmt.Errorf("mockfeed: marshal event: %w", err))
	}
	return b
}

func main() {
	addr := flag.String("addr", ":8090", "listen address")
	flag.Parse()

	h := newHub()
	mux := http.NewServeMux()

	// Match the real DOMjudge path: /api/v4/contests/{cid}/event-feed.
	// We don't validate the contest id — any path ending in /event-feed works.
	mux.HandleFunc("/api/v4/contests/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/event-feed") {
			http.NotFound(w, r)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		sub, cancel := h.subscribe()
		defer cancel()
		log.Printf("subscriber connected: %s?%s", r.URL.Path, r.URL.RawQuery)

		// Periodic comment keepalive so proxies/loadbalancers don't close idle
		// connections. Real DOMjudge does not do this; harmless here.
		keepalive := time.NewTicker(15 * time.Second)
		defer keepalive.Stop()

		for {
			select {
			case <-r.Context().Done():
				log.Printf("subscriber disconnected")
				return
			case line, ok := <-sub.ch:
				if !ok {
					return
				}
				if _, err := w.Write(append(line, '\n')); err != nil {
					return
				}
				flusher.Flush()
			case <-keepalive.C:
				if _, err := w.Write([]byte("\n")); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	})

	// Developer trigger. POST /trigger?type=judgements broadcasts one event line.
	// Default type is "judgements" since that's what fires the balloon flow.
	mux.HandleFunc("/trigger", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		t := r.URL.Query().Get("type")
		if t == "" {
			t = "judgements"
		}
		line := h.newEvent(t)
		n := h.broadcast(line)
		log.Printf("trigger: emitted %s event to %d subscriber(s): %s", t, n, line)
		w.Header().Set("Content-Type", "application/json")
		// Response is best-effort feedback for the dev who poked /trigger; a
		// write failure here only means their curl was canceled.
		_, _ = fmt.Fprintf(w, `{"delivered":%d,"line":%s}`+"\n", n, line)
	})

	log.Printf("mockfeed listening on %s", *addr)
	log.Printf("stream:  GET  http://localhost%s/api/v4/contests/<cid>/event-feed?stream=true", *addr)
	log.Printf("trigger: POST http://localhost%s/trigger[?type=judgements|balloons|state]", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}
