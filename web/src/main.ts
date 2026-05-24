import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import {
  BalloonService,
  StreamBalloonsResponse_Kind,
} from "./gen/balloons/v1/balloons_pb.js";
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
const themeToggle = document.getElementById("theme-toggle")!;
const themeToggleIcon = document.getElementById("theme-toggle-icon")!;

const state = new Map<string, Balloon>();

function applyTheme(theme: "light" | "dark") {
  document.documentElement.classList.toggle("light", theme === "light");
  themeToggleIcon.textContent = theme === "light" ? "🌙" : "☀️";
}

applyTheme(
  (() => {
    try {
      return localStorage.getItem("theme") === "light" ? "light" : "dark";
    } catch {
      return "dark";
    }
  })(),
);

themeToggle.onclick = () => {
  const next = document.documentElement.classList.contains("light")
    ? "dark"
    : "light";
  try {
    localStorage.setItem("theme", next);
  } catch {}
  applyTheme(next);
};

type Status = "connecting" | "connected" | "disconnected";
function setStatus(s: Status) {
  statusDot.className =
    "h-2 w-2 rounded-full " +
    {
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

function row(b: Balloon): HTMLElement {
  const card = document.createElement("div");
  card.className =
    "flex items-center gap-4 rounded-lg border border-zinc-800 bg-zinc-900/40 px-4 py-3 light:border-zinc-200 light:bg-white";
  if (b.done) card.classList.add("opacity-60");

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
    sub.className = "text-sm text-zinc-400 light:text-zinc-500";
    sub.textContent = `First team to solve problem ${b.problemLabel}!`;
    meta.appendChild(sub);
  }

  card.appendChild(meta);

  const actions = document.createElement("div");
  actions.className = "flex shrink-0 items-center gap-2";

  // Reprint is always offered — on pending tickets it covers "the original got
  // lost"; on delivered tickets it's a manual test hook (no DOMjudge state is
  // touched, only a new ticket is printed).
  const reprintBtn = document.createElement("button");
  reprintBtn.className =
    "rounded-md border border-zinc-700 bg-zinc-800/60 px-3 py-1.5 text-sm font-medium text-zinc-200 hover:bg-zinc-800 disabled:cursor-progress disabled:opacity-60 light:border-zinc-300 light:bg-zinc-100 light:text-zinc-700 light:hover:bg-zinc-200";
  reprintBtn.textContent = "Reprint";
  reprintBtn.title = b.done
    ? "Re-print this ticket (test hook — delivery state is not affected)"
    : "Print this ticket again (use if the original got lost)";
  reprintBtn.onclick = async () => {
    reprintBtn.disabled = true;
    reprintBtn.textContent = "reprinting…";
    try {
      await client.reprint({ balloonId: b.id });
    } catch (err) {
      console.error("reprint:", err);
    } finally {
      reprintBtn.disabled = false;
      reprintBtn.textContent = "Reprint";
    }
  };
  actions.appendChild(reprintBtn);

  if (!b.done) {
    const btn = document.createElement("button");
    btn.className =
      "rounded-md border border-emerald-700/50 bg-emerald-900/30 px-3 py-1.5 text-sm font-medium text-emerald-300 hover:bg-emerald-900/60 disabled:cursor-progress disabled:opacity-60 light:border-emerald-300 light:bg-emerald-100 light:text-emerald-800 light:hover:bg-emerald-200";
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
    actions.appendChild(btn);
  }

  card.appendChild(actions);
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
  for (const b of pending) pendingEl.appendChild(row(b));
  pendingEmptyEl.classList.toggle("hidden", pending.length !== 0);

  deliveredEl.innerHTML = "";
  for (const b of delivered) deliveredEl.appendChild(row(b));
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
