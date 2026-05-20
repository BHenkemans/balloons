# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Purpose

Custom replacement for DOMjudge's built-in balloon tool, written for the GEHACK contest. The built-in tool doesn't surface a first-solve flag and can't drive a receipt printer; this tool exists to fix both. A future contest-area map will also consume data from this service. Receipt-printer and map integrations are deliberately out of scope for the current code.

## Dev environment

Everything (Go, buf, protoc plugins, Node, Tailwind) is pinned in `shell.nix`. Enter with `nix-shell`, or wrap one-off commands with `nix-shell --run "..."`. Commands below assume the shell is active.

Required env vars (see `.env.example`): `DOMJUDGE_URL`, `DOMJUDGE_USER`, `DOMJUDGE_PASS`, `DOMJUDGE_CONTEST_ID`. Load with `set -a; source .env; set +a` before running the server. Optional: `ADDR` (default `:8080`).

## Common commands

A `justfile` wraps everything. `just` lists recipes; the useful ones are `just bootstrap` (first-time setup), `just gen` (regen Go + TS from proto), `just build-web`, `just watch`, `just run`, `just fmt`, `just lint`, `just ping` (curl the server). `set dotenv-load` is on so `.env` is read automatically for any recipe.

There are no tests yet.

## Architecture

**Single Go binary** at `cmd/server` serves both the connectRPC API and the static frontend on one port. `mux.Handle(path, handler)` mounts connectRPC; `mux.Handle("/", http.FileServer(http.Dir("web")))` serves `web/index.html` and `web/dist/*` for everything else.

**Hub pattern** (`internal/server/hub.go`) is the cache + fan-out:
- `Run(ctx)` does an initial `refresh()` then spawns `runEventFeed()` and serializes subsequent refreshes off a buffered `trigger` channel.
- `refresh()` fetches `/balloons` + `/awards` from DOMjudge, rebuilds the proto state, diffs against the previous map, and broadcasts `KIND_ADDED`/`KIND_UPDATED` events to all subscribers.
- `runEventFeed()` holds a long-lived NDJSON read on `/api/v4/contests/{cid}/event-feed?stream=true&types=judgements,awards,balloons` and calls `TriggerRefresh()` on every line. Reconnect with exponential backoff up to 30s.
- Subscribers get a snapshot + a buffered channel from `Subscribe()`. Slow subscribers get force-closed (they reconnect and pick up a fresh snapshot).

`MarkDone` calls DOMjudge then `TriggerRefresh()` so the UI sees the change within one round-trip instead of waiting for the event-feed.

**Frontend** is a single HTML page + bundled TS (`web/src/main.ts`). It opens a server-streaming `StreamBalloons` RPC and renders straight from event deltas (no `ListBalloons` call in the happy path). On stream error it reconnects with the same exponential backoff. State is a `Map<string, Balloon>` keyed by id-as-string.

## DOMjudge integration gotchas

These cost time to figure out — keep them in mind:

- **First-solve is not in `/balloons`.** Derive from `/awards`: each award with `id` starting `first-to-solve-{problem_id}` carries the team(s) holding it. Key the lookup as `problem_id + "|" + team_id` (`buildFirstSolveSet` in `internal/server/server.go`).
- **Award IDs use the problem's internal id, not the label.** `first-to-solve-covencomplications`, not `first-to-solve-C`. So look up against `ContestProblem.ID`, not `.Label`.
- **`done` is one-way.** DOMjudge only exposes `POST /balloons/{id}/done` — there is no unmark endpoint in the API. Current behavior matches that limitation; if undo becomes a requirement, track delivered state locally rather than reverse-engineering the admin web route.
- **`team` is `{label}: {name}`.** DOMjudge prepends the team's label (number or string) to the display name. Stripped server-side in `toProto` with `^\S+:\s+`.
- **Event-feed events are triggers, not deltas.** The code treats any event as "something changed, refetch and diff." Don't try to interpret event payloads — `/balloons` + `/awards` is canonical.

## Proto / wire surface

`proto/balloons/v1/balloons.proto` is deliberately minimal: 6 fields on `Balloon` (`id`, `problem_label`, `problem_rgb`, `team_name`, `done`, `first_solve`). The server holds more DOMjudge data internally but doesn't put it on the wire. Add fields when a consumer needs them, not preemptively.

Generated code (`gen/` for Go, `web/src/gen/` for TS) is gitignored. Always run `buf generate` after editing the proto.

## What's where

```
proto/balloons/v1/         schema (single file)
gen/                       generated Go (gitignored)
web/src/gen/               generated TS (gitignored)
cmd/server/                main.go — env config + http.Server + hub.Run()
internal/domjudge/         REST + event-feed client; mirrors DOMjudge JSON shapes
internal/server/           connectRPC handlers + Hub
web/                       Tailwind v4 + esbuild + connect-web frontend
```
