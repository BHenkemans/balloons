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
const scanForm = document.getElementById("scan-form") as HTMLFormElement;
const scanInput = document.getElementById("scan-input") as HTMLInputElement;
const scanHint = document.getElementById("scan-hint")!;
const toastStack = document.getElementById("toast-stack")!;

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

function makeBalloonCircle(b: Balloon, size: "sm" | "md"): HTMLElement {
  const sizeClass = size === "sm" ? "h-8 w-8 text-xs" : "h-10 w-10";
  const ball = document.createElement("div");
  ball.className = `flex ${sizeClass} shrink-0 items-center justify-center rounded-full font-bold text-white shadow ring-1 ring-black/20`;
  ball.style.background = b.problemRgb || "#888";
  ball.textContent = b.problemLabel || "?";
  return ball;
}

function row(b: Balloon): HTMLElement {
  const card = document.createElement("div");
  card.className =
    "flex items-center gap-4 rounded-lg border border-zinc-800 bg-zinc-900/40 px-4 py-3 light:border-zinc-200 light:bg-white";
  if (b.done) card.classList.add("opacity-60");

  card.appendChild(makeBalloonCircle(b, "md"));

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

function parseScan(raw: string): string | null {
  const s = raw.trim();
  if (!s) return null;
  if (/^\d+$/.test(s)) return s;
  try {
    const id = new URL(s, "http://x").searchParams.get("id");
    if (id && /^\d+$/.test(id)) return id;
  } catch {}
  return null;
}

function flashHint(msg: string, kind: "ok" | "err") {
  scanHint.textContent = msg;
  scanHint.classList.remove("hidden", "text-emerald-400", "text-red-400");
  scanHint.classList.add(kind === "ok" ? "text-emerald-400" : "text-red-400");
  window.setTimeout(() => scanHint.classList.add("hidden"), 2500);
}

const UNDO_MS = 4000;
const pendingDeliveries = new Set<string>();

function showDeliverToast(b: Balloon) {
  const idStr = b.id.toString();
  if (pendingDeliveries.has(idStr)) return;
  pendingDeliveries.add(idStr);

  const toast = document.createElement("div");
  toast.className =
    "flex items-start gap-3 rounded-lg border border-emerald-700/50 bg-emerald-950/90 px-4 py-3 text-sm text-emerald-100 shadow-lg backdrop-blur light:border-emerald-300 light:bg-emerald-50 light:text-emerald-900";
  toast.appendChild(makeBalloonCircle(b, "sm"));

  const body = document.createElement("div");
  body.className = "min-w-0 flex-1";
  const title = document.createElement("div");
  title.className = "truncate font-semibold";
  title.textContent = `Delivering: ${b.teamName}`;
  body.appendChild(title);
  const sub = document.createElement("div");
  sub.className = "text-xs text-emerald-300/80 light:text-emerald-700";
  sub.textContent = `Problem ${b.problemLabel}`;
  body.appendChild(sub);
  toast.appendChild(body);

  const cancelBtn = document.createElement("button");
  cancelBtn.type = "button";
  cancelBtn.className =
    "shrink-0 rounded-md border border-emerald-700 bg-emerald-900/40 px-2 py-1 text-xs font-medium hover:bg-emerald-900/70 light:border-emerald-400 light:bg-white light:text-emerald-800 light:hover:bg-emerald-100";
  toast.appendChild(cancelBtn);
  toastStack.appendChild(toast);

  const startedAt = Date.now();
  const updateLabel = () => {
    const remaining = Math.max(0, UNDO_MS - (Date.now() - startedAt));
    cancelBtn.textContent = `Cancel (${Math.ceil(remaining / 1000)}s)`;
  };
  updateLabel();
  const tick = window.setInterval(updateLabel, 200);

  const finish = () => {
    window.clearInterval(tick);
    window.clearTimeout(commit);
    pendingDeliveries.delete(idStr);
    toast.remove();
  };

  cancelBtn.onclick = () => {
    finish();
    flashHint(`Cancelled ${b.teamName}`, "ok");
  };

  const commit = window.setTimeout(async () => {
    window.clearInterval(tick);
    // Stream may have flipped the balloon to done while we waited (another
    // runner, another tab) — don't fire a redundant MarkDone.
    if (state.get(idStr)?.done) {
      finish();
      return;
    }
    cancelBtn.disabled = true;
    cancelBtn.textContent = "delivering…";
    try {
      await client.markDone({ balloonId: b.id });
      finish();
    } catch (err) {
      console.error("markDone:", err);
      pendingDeliveries.delete(idStr);
      cancelBtn.disabled = false;
      cancelBtn.textContent = "retry";
      cancelBtn.onclick = () => {
        toast.remove();
        showDeliverToast(b);
      };
    }
  }, UNDO_MS);
}

function handleScan(raw: string) {
  const id = parseScan(raw);
  if (!id) {
    flashHint("Unrecognized scan", "err");
    return;
  }
  const b = state.get(id);
  if (!b) {
    flashHint(`Unknown balloon #${id}`, "err");
    return;
  }
  if (b.done) {
    flashHint(`Already delivered: ${b.teamName}`, "err");
    return;
  }
  showDeliverToast(b);
}

scanForm.addEventListener("submit", (e) => {
  e.preventDefault();
  const v = scanInput.value;
  scanInput.value = "";
  handleScan(v);
});

// Hand scanners act as keyboards, so the scan input has to stay focused —
// but don't steal focus from interactive elements (buttons, other inputs),
// otherwise tabbing and clicks land somewhere the user didn't intend.
function focusScan() {
  const a = document.activeElement;
  if (a === scanInput) return;
  if (a && a !== document.body) return;
  scanInput.focus();
}
focusScan();
scanInput.addEventListener("blur", () => {
  window.setTimeout(focusScan, 0);
});
window.addEventListener("focus", focusScan);

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
