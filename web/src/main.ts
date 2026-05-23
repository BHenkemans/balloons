import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { BalloonService, StreamBalloonsResponse_Kind } from "./gen/balloons/v1/balloons_pb.js";
import type { Balloon } from "./gen/balloons/v1/balloons_pb.js";

const transport = createConnectTransport({ baseUrl: window.location.origin });
const client = createClient(BalloonService, transport);

const pendingEl = document.getElementById("pending")!;
const pendingEmptyEl = document.getElementById("pending-empty")!;
const pendingCountEl = document.getElementById("pending-count")!;
const deliveredEl = document.getElementById("delivered")!;
const deliveredEmptyEl = document.getElementById("delivered-empty")!;
const deliveredCountEl = document.getElementById("delivered-count")!;
const statusDot = document.getElementById("status-dot")!;
const statusText = document.getElementById("status-text")!;
const freezeBanner = document.getElementById("freeze-banner")!;

const state = new Map<string, Balloon>();

type Status = "connecting" | "connected" | "disconnected";
function setStatus(s: Status) {
  statusDot.className = "h-2 w-2 rounded-full " + {
    connecting: "bg-amber-400 animate-pulse",
    connected: "bg-emerald-500",
    disconnected: "bg-red-500",
  }[s];
  statusText.textContent = {
    connecting: "Connecting…",
    connected: "Connected",
    disconnected: "Disconnected",
  }[s];
}

function row(b: Balloon, withButton: boolean): HTMLElement {
  const card = document.createElement("div");
  card.className =
    "flex items-center gap-4 rounded-lg border border-zinc-800 bg-zinc-900/40 px-4 py-3";
  if (!withButton) card.classList.add("opacity-60");

  const ball = document.createElement("div");
  ball.className =
    "flex h-10 w-10 shrink-0 items-center justify-center rounded-full font-bold text-white shadow ring-1 ring-black/20";
  ball.style.background = b.problemRgb || "#888";
  ball.textContent = b.problemLabel || "?";
  card.appendChild(ball);

  const meta = document.createElement("div");
  meta.className = "min-w-0 flex-1";

  const team = document.createElement("div");
  team.className = "truncate font-semibold";
  team.textContent = b.teamName;
  meta.appendChild(team);

  if (b.firstSolve) {
    const sub = document.createElement("div");
    sub.className = "text-sm text-zinc-400";
    sub.textContent = `First team to solve problem ${b.problemLabel}!`;
    meta.appendChild(sub);
  }

  card.appendChild(meta);

  if (withButton) {
    const btn = document.createElement("button");
    btn.className =
      "shrink-0 rounded-md border border-emerald-700/50 bg-emerald-900/30 px-3 py-1.5 text-sm font-medium text-emerald-300 hover:bg-emerald-900/60 disabled:cursor-progress disabled:opacity-60";
    btn.textContent = "Deliver";
    btn.onclick = async () => {
      btn.disabled = true;
      btn.textContent = "delivering…";
      try {
        await client.markDone({ balloonId: b.id });
      } catch (err) {
        console.error("markDone:", err);
        btn.disabled = false;
        btn.textContent = "Deliver";
      }
    };
    card.appendChild(btn);
  }

  return card;
}

function render() {
  const all = [...state.values()];
  const byNewest = (a: Balloon, b: Balloon) => Number(b.id) - Number(a.id);
  const pending = all.filter((b) => !b.done).sort(byNewest);
  const delivered = all.filter((b) => b.done).sort(byNewest);

  pendingCountEl.textContent = `(${pending.length})`;
  deliveredCountEl.textContent = `(${delivered.length})`;

  pendingEl.innerHTML = "";
  for (const b of pending) pendingEl.appendChild(row(b, true));
  pendingEmptyEl.classList.toggle("hidden", pending.length !== 0);

  deliveredEl.innerHTML = "";
  for (const b of delivered) deliveredEl.appendChild(row(b, false));
  deliveredEmptyEl.classList.toggle("hidden", delivered.length !== 0);
}

async function stream() {
  for await (const ev of client.streamBalloons({})) {
    setStatus("connected");
    if (ev.kind === StreamBalloonsResponse_Kind.FREEZE) {
      freezeBanner.classList.toggle("hidden", !ev.frozen);
      continue;
    }
    if (!ev.balloon) continue;
    state.set(ev.balloon.id.toString(), ev.balloon);
    render();
  }
}

async function runForever() {
  let backoff = 1000;
  while (true) {
    setStatus("connecting");
    try {
      await stream();
      backoff = 1000;
    } catch (err) {
      console.error("stream error:", err);
      setStatus("disconnected");
      await new Promise((r) => setTimeout(r, backoff));
      backoff = Math.min(backoff * 2, 15000);
    }
  }
}

runForever();
