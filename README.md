# balloons

A custom balloon-runner tool for the GEHACK programming contest. Replaces DOMjudge's built-in balloon tool with two features it doesn't have: a **first-solve** highlight and **printed tickets** (IPP or thermal/ESC-POS), with a per-ticket **QR code** that lets a runner mark the balloon delivered from their phone.

```
DOMjudge ──► event-feed ──► Hub ──► gRPC stream ──► Web UI (operator)
                            │
                            ├──► Printer (IPP / ESC-POS / noop)
                            │       └─ Typst ticket w/ QR
                            │
                            └──► /scan?id=<n>  ◄── runner's phone (scan)
```

## Features

- **Single binary, single port.** Go server on `:8080` serves both the connectRPC API and the static frontend.
- **Real-time updates.** A long-lived event-feed connection to DOMjudge triggers refreshes; subscribers get diff events (`ADDED` / `UPDATED` / `FREEZE`) over a server-streaming RPC.
- **First-solve flag**, derived from `/balloons` itself (DOMjudge's `/awards` endpoint is empty during a live contest).
- **Group filters**: hide balloons for certain DOMjudge groups (`HIDE_GROUP_IDS`) and / or strip the first-solve flag for company / sponsor teams (`NO_FIRST_SOLVE_GROUP_IDS`).
- **Pluggable printer subsystem**: `noop` (logs only), `ipp` (Typst → PDF → IPP), or `escpos` (Typst → PNG → 1-bit raster over TCP to a thermal printer at port 9100). Reprint dedupe is backed by a local SQLite store, so restarts don't reprint already-printed tickets.
- **Scan-to-deliver flow**: every printed ticket carries a QR linking to `/scan?id=<n>`. Scanning shows "Delivered ✓" with a 5-second Undo timer, then commits a `MarkDone` to DOMjudge. The "undo before commit" model is deliberate — DOMjudge's `done` is one-way.
- **Freeze-aware UI**: scoreboard freeze state is broadcast as a dedicated event and reflected in the operator view.

## Project layout

```
proto/balloons/v1/         schema (single file)
gen/                       generated Go (gitignored)
web/src/gen/               generated TS (gitignored)
cmd/server/                main.go — env config + http.Server + hub.Run()
internal/domjudge/         REST + event-feed client; mirrors DOMjudge JSON shapes
internal/server/           connectRPC handlers + Hub
internal/printer/          Printer interface + Noop / IPP / ESCPOS impls
internal/state/            SQLite-backed ticket-state store (printed_at, delivered_at)
internal/config/           tiny env-var helpers
templates/                 Typst ticket template (single file, themed by --input)
web/                       Tailwind v4 + esbuild + connect-web frontend
web/scan.html              stand-alone runner-facing scan page
```

## Quickstart

### 1. Enter the dev shell

Everything — Go, `buf`, the protoc plugins, Node, and Typst — is pinned in `shell.nix`:

```bash
nix-shell
```

(Or wrap one-off commands with `nix-shell --run "..."` if you'd rather not enter the shell.)

### 2. Configure

Copy `.env.example` to `.env` and fill in the four required DOMjudge fields:

```bash
cp .env.example .env
$EDITOR .env
```

Required:

| Variable | Description |
| --- | --- |
| `DOMJUDGE_URL` | Base URL of the DOMjudge API, e.g. `https://judge.example.com` |
| `DOMJUDGE_USER` | API user with balloon access |
| `DOMJUDGE_PASS` | API password |
| `DOMJUDGE_CONTEST_ID` | Numeric contest id |

Optional — see `.env.example` for the full annotated list. The most commonly tweaked:

| Variable | Default | Purpose |
| --- | --- | --- |
| `ADDR` | `:8080` | Listen address |
| `HIDE_GROUP_IDS` | — | CSV of group ids whose balloons disappear entirely |
| `NO_FIRST_SOLVE_GROUP_IDS` | — | CSV of group ids whose teams get balloons but never the first-solve flag |
| `PRINTER_KIND` | `noop` | `noop`, `ipp`, or `escpos` |
| `PRINTER_IPP_URI` | — | Full `ipp://host:port/queue` URI (required for `ipp`) |
| `PRINTER_ESCPOS_ADDR` | — | `host:port` of the thermal printer's raw socket (required for `escpos`) |
| `PRINTER_ESCPOS_WIDTH` | `576` | Head width in dots (576 = 80mm @ 203dpi; 384 for 58mm printers) |
| `PRINTER_TEMPLATE` | `templates/balloon.typ` | Typst template path. The same file handles both themes — the printer driver passes `--input theme=color` (IPP) or `--input theme=thermal` (ESC/POS) |
| `STATE_DB` | `balloons.db` | SQLite file tracking `printed_at` / `delivered_at` |
| `CONTEST_TZ` | `time.Local` | IANA timezone (e.g. `Europe/Amsterdam`) used to render the ticket datetime. Set explicitly when the server runs in a container/systemd unit that inherits UTC. |
| `SCAN_BASE_URL` | `http://<hostname><ADDR>` | Public base URL used to build the per-ticket QR code |

### 3. Bootstrap, generate, build

```bash
just bootstrap   # npm install + buf generate + build the frontend
```

### 4. Run

```bash
just run         # loads .env automatically (set dotenv-load is on)
```

Open <http://localhost:8080> for the operator view. The UI opens a streaming RPC and renders straight from event deltas, so balloons appear within one DOMjudge round-trip.

Smoke test:

```bash
just ping
```

## Common commands

`just` lists everything:

| Recipe | Purpose |
| --- | --- |
| `just bootstrap` | First-time setup (deps + codegen + web build) |
| `just gen` | Regenerate Go + TS from `balloons.proto` (run after editing the schema) |
| `just build-web` | Build the frontend bundle once |
| `just watch` | Rebuild CSS + JS on change |
| `just run` | Run the server |
| `just build` | Build everything for release (`bin/server` + `web/dist/*`) |
| `just fmt` | Format protobuf and Go |
| `just lint` | `buf lint` on the protos |
| `just vet` | `go vet ./...` |
| `just tidy` | `go mod tidy` |
| `just ping` | curl `ListBalloons` against the running server |
| `just clean` | Wipe generated + built artifacts |

## Tests

```bash
go test ./...
```

Coverage is intentionally light — `internal/state` and `internal/server` have unit tests; everything else is exercised by pointing at a live DOMjudge contest. To re-trigger a print without waiting for a real submission, use the **Reprint** button in the UI: it clears the local `printed_at` row and re-dispatches the print goroutine.

## Architecture

### Hub (`internal/server/hub.go`)

The Hub is the cache + fan-out:

- `Run(ctx)` does an initial `refresh()` then spawns `runEventFeed()` and serializes subsequent refreshes off a buffered `trigger` channel.
- `refresh()` fetches `/balloons`, `/teams`, `/state`, and `/problems` from DOMjudge, applies group filters, computes first-solve set, builds a `snapshot`, diffs it against the existing in-memory state, and broadcasts events to subscribers. Per-team delivery / in-delivery sets and the full problem-label strip are precomputed in the snapshot so dispatched print goroutines don't need the hub lock.
- `runEventFeed()` holds a long-lived NDJSON read on `/api/v4/contests/{cid}/event-feed?stream=true&types=judgements,balloons,state` and calls `TriggerRefresh()` on every line. Reconnect with exponential backoff up to 30s.
- `Subscribe()` returns a snapshot + a buffered channel. Slow subscribers get force-closed and will reconnect into a fresh snapshot.
- `MarkDone(id)` POSTs to DOMjudge, records local delivery, then `TriggerRefresh()` so the UI sees the change in one round-trip instead of waiting for the next event-feed tick.

### Printer subsystem (`internal/printer/`)

`Printer { Print(ctx, Ticket) error }` with three implementations:

- **Noop** — logs only. Default.
- **IPP** — renders `templates/balloon.typ` with `--input theme=color` to PDF via the `typst` CLI, then submits it to an IPP queue via `phin1x/go-ipp`.
- **ESCPOS** — renders `templates/balloon.typ` with `--input theme=thermal` to a PNG at 2× supersampling, area-filters down to the configured dot width, converts each pixel to 1-bit using a chroma-aware ink-density pass (saturated colors → solid black; near-grayscale → Floyd-Steinberg), then streams the result as `GS v 0` raster chunks plus a partial-cut over a TCP raw socket (typically port 9100). The Typst page width is derived as `width / 203dpi` and passed to the template via `--input page_width_mm=...`, so adjusting `PRINTER_ESCPOS_WIDTH` doesn't require template edits.

Reprints are gated by the **state store** (`internal/state`, a small SQLite table): `Hub.print` calls `Store.IsPrinted(id)` before printing and `Store.RecordPrinted(id)` after. Restarting the server never reprints already-printed balloons, and concurrent refreshes can't double-print. Deleting `STATE_DB` resets the dedupe.

### Scan-to-deliver flow

Every printed ticket carries a QR encoding `<SCAN_BASE_URL>/scan?id=<n>`. `web/scan.html` is intentionally a stand-alone page (no shared bundle, no connect-web SDK) so a phone with weak signal can load it fast — it calls `ListBalloons` and `MarkDone` directly as plain Connect-protocol JSON POSTs (`fetch("/balloons.v1.BalloonService/MarkDone", ...)`).

On scan:

1. The page shows **Delivered ✓** immediately and starts a 5-second countdown.
2. If the runner taps **Undo** within 5s, nothing is sent.
3. Otherwise `MarkDone` fires once the timer expires.

The 5-second buffer matters because DOMjudge's `done` is one-way (no unmark in the API) — once we POST the mark, we can't take it back, so the only honest "undo" is "cancel before we commit." A balloon that's already `done` at scan time shows **Already delivered** with no countdown.

If `SCAN_BASE_URL` is unset, it falls back to `http://<os.Hostname()><ADDR>` — useful on a contest LAN where runner phones can reach the server by hostname. Set it explicitly behind a reverse proxy or when phones can't resolve the host.

### Frontend (`web/`)

Single HTML page + bundled TS (`web/src/main.ts`), Tailwind v4 via the `@tailwindcss/cli`, bundled with esbuild. Opens a server-streaming `StreamBalloons` RPC and renders straight from event deltas (no `ListBalloons` call in the happy path). On stream error it reconnects with the same exponential backoff. State is a `Map<string, Balloon>` keyed by id-as-string (connect-protocol JSON encodes int64 as a string).

## DOMjudge integration gotchas

These cost time to figure out, so they're documented here:

- **First-solve must be derived from `/balloons`.** `/awards` is empty during a live contest. Per problem, the balloon with the earliest `time` is the first solve. The `time` field is a fixed-width seconds.nanoseconds string, so lexical compare is correct. Teams in `NO_FIRST_SOLVE_GROUP_IDS` are skipped — if a sponsor team solves first, the next eligible team gets the flag.
- **Team groups come from `/teams`, not `/balloons`.** The balloon JSON has `categoryid: null` even when the team has a category. Group filters match if **any** of a team's `group_ids` is in the filter set.
- **`done` is one-way.** DOMjudge only exposes `POST /balloons/{id}/done`; there's no unmark endpoint. The 5-second "undo" on the scan page is "cancel before we commit," not a reversal.
- **`team` is `"{label}: {name}"`.** DOMjudge prepends the team's label (number or string) to the display name; stripped server-side in `toProto` with `^\S+:\s+`.
- **Event-feed events are triggers, not deltas.** The code treats any event as "something changed, refetch and diff." Don't try to interpret event payloads — `/balloons`, `/teams`, and `/state` are canonical.
- **Freeze detection comes from `/state`.** Scoreboard freeze is active when `frozen != null && thawed == null`. The Hub broadcasts a `KIND_FREEZE` event on transitions and on every new subscription so reloads pick up the current state.

## Proto / wire surface

`proto/balloons/v1/balloons.proto` is deliberately minimal: 6 fields on `Balloon` (`id`, `problem_label`, `problem_rgb`, `team_name`, `done`, `first_solve`). `StreamBalloonsResponse` carries a `Kind` (`ADDED` / `UPDATED` / `FREEZE`) plus an optional `balloon` and a `frozen` bool used only on `KIND_FREEZE`. The server holds more DOMjudge data internally but doesn't put it on the wire. Add fields when a consumer needs them — not preemptively.

Generated code (`gen/` for Go, `web/src/gen/` for TS) is **gitignored** — always run `just gen` (or `buf generate`) after editing the proto.

| RPC | Direction | Purpose |
| --- | --- | --- |
| `ListBalloons` | unary | Full snapshot, used by `scan.html` and on cold reconnects |
| `StreamBalloons` | server-stream | Snapshot + live diff events |
| `MarkDone` | unary | Mark a balloon delivered (proxies to DOMjudge) |
| `Reprint` | unary | Re-dispatch a print without waiting for a fresh submission |

## License

Internal contest tool. Adapt freely for your own event.
