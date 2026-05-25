import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import {
  BalloonService,
  RunnerStatus,
  StreamBalloonsResponse_Kind,
  StreamRunnersResponse_Kind,
} from "./gen/balloons/v1/balloons_pb.js";
import type {
  Balloon,
  Runner,
} from "./gen/balloons/v1/balloons_pb.js";

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
const pendingAdmitSection = document.getElementById("pending-admit-section")!;
const pendingAdmitEl = document.getElementById("pending-admit")!;
const pendingAdmitCountEl = document.getElementById("pending-admit-count")!;
const rosterEl = document.getElementById("roster")!;
const rosterEmptyEl = document.getElementById("roster-empty")!;
const rosterCountEl = document.getElementById("roster-count")!;

const state = new Map<string, Balloon>();
const runners = new Map<string, Runner>();

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

  if (b.assignedRunnerName) {
    const sub = document.createElement("div");
    sub.className = "text-sm text-emerald-400 light:text-emerald-700";
    sub.textContent = `Assigned to: ${b.assignedRunnerName}`;
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
    const u = new URL(s);
    const id = u.searchParams.get("id");
    if (id && /^\d+$/.test(id)) return id;
  } catch {}
  const m = s.match(/[?&]id=(\d+)/);
  return m ? m[1] : null;
}

function flashHint(msg: string, kind: "ok" | "err") {
  scanHint.textContent = msg;
  scanHint.classList.remove("hidden", "text-emerald-400", "text-red-400");
  scanHint.classList.add(kind === "ok" ? "text-emerald-400" : "text-red-400");
  window.setTimeout(() => scanHint.classList.add("hidden"), 2500);
}

const UNDO_MS = 4000;

function showDeliverToast(b: Balloon) {
  const toast = document.createElement("div");
  toast.className =
    "flex items-start gap-3 rounded-lg border border-emerald-700/50 bg-emerald-950/90 px-4 py-3 text-sm text-emerald-100 shadow-lg backdrop-blur light:border-emerald-300 light:bg-emerald-50 light:text-emerald-900";

  const ball = document.createElement("div");
  ball.className =
    "flex h-8 w-8 shrink-0 items-center justify-center rounded-full text-xs font-bold text-white shadow ring-1 ring-black/20";
  ball.style.background = b.problemRgb || "#888";
  ball.textContent = b.problemLabel || "?";
  toast.appendChild(ball);

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
  cancelBtn.textContent = `Cancel (${Math.ceil(UNDO_MS / 1000)}s)`;
  toast.appendChild(cancelBtn);

  toastStack.appendChild(toast);

  let cancelled = false;
  const startedAt = Date.now();
  const tick = window.setInterval(() => {
    const remaining = UNDO_MS - (Date.now() - startedAt);
    if (remaining <= 0) {
      window.clearInterval(tick);
      return;
    }
    cancelBtn.textContent = `Cancel (${Math.ceil(remaining / 1000)}s)`;
  }, 200);

  cancelBtn.onclick = () => {
    cancelled = true;
    window.clearInterval(tick);
    toast.remove();
    flashHint(`Cancelled ${b.teamName}`, "err");
  };

  window.setTimeout(async () => {
    window.clearInterval(tick);
    if (cancelled) return;
    cancelBtn.disabled = true;
    cancelBtn.textContent = "delivering…";
    try {
      await client.markDone({ balloonId: b.id });
      toast.remove();
    } catch (err) {
      console.error("markDone:", err);
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

// Keep the scan field focused so hand scanners (which act as keyboards)
// always land in the right place. Buttons still receive clicks because the
// click fires before the refocus runs.
function focusScan() {
  if (document.activeElement !== scanInput) scanInput.focus();
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

// --- Runner roster + pending admissions ---

function fmtAgo(iso: string): string {
  if (!iso) return "";
  const ms = Date.now() - Date.parse(iso);
  if (Number.isNaN(ms) || ms < 0) return "";
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  return `${h}h ago`;
}

function statusBadge(s: RunnerStatus): { text: string; cls: string } {
  switch (s) {
    case RunnerStatus.PENDING_ADMIT:
      return { text: "pending admit", cls: "bg-amber-900/40 text-amber-300 light:bg-amber-100 light:text-amber-800" };
    case RunnerStatus.IDLE:
      return { text: "idle", cls: "bg-zinc-800 text-zinc-300 light:bg-zinc-200 light:text-zinc-700" };
    case RunnerStatus.AVAILABLE:
      return { text: "available", cls: "bg-emerald-900/40 text-emerald-300 light:bg-emerald-100 light:text-emerald-800" };
    case RunnerStatus.BUSY:
      return { text: "busy", cls: "bg-sky-900/40 text-sky-300 light:bg-sky-100 light:text-sky-800" };
    case RunnerStatus.DELIVERED_PENDING_ACK:
      return { text: "delivered, waiting", cls: "bg-sky-900/40 text-sky-300 light:bg-sky-100 light:text-sky-800" };
    case RunnerStatus.REJECTED:
      return { text: "rejected", cls: "bg-red-900/40 text-red-300 light:bg-red-100 light:text-red-800" };
    case RunnerStatus.OFFLINE:
      return { text: "offline", cls: "bg-zinc-800 text-zinc-500 light:bg-zinc-200 light:text-zinc-500" };
    default:
      return { text: "?", cls: "bg-zinc-800 text-zinc-300 light:bg-zinc-200 light:text-zinc-700" };
  }
}

function admitRow(r: Runner): HTMLElement {
  const card = document.createElement("div");
  card.className =
    "flex items-center gap-3 rounded-lg border border-amber-700/50 bg-amber-900/20 px-4 py-3 light:border-amber-300 light:bg-amber-50";
  const meta = document.createElement("div");
  meta.className = "min-w-0 flex-1";
  const name = document.createElement("div");
  name.className = "font-semibold";
  name.textContent = r.name;
  const sub = document.createElement("div");
  sub.className = "text-xs text-zinc-400 light:text-zinc-500";
  sub.textContent = `requested ${fmtAgo(r.createdAt)}`;
  meta.appendChild(name);
  meta.appendChild(sub);
  card.appendChild(meta);

  const admitBtn = document.createElement("button");
  admitBtn.className =
    "rounded-md border border-emerald-700/50 bg-emerald-900/30 px-3 py-1.5 text-sm font-medium text-emerald-300 hover:bg-emerald-900/60 disabled:cursor-progress disabled:opacity-60 light:border-emerald-300 light:bg-emerald-100 light:text-emerald-800 light:hover:bg-emerald-200";
  admitBtn.textContent = "Admit";
  admitBtn.onclick = async () => {
    admitBtn.disabled = true;
    try {
      await client.admitRunner({ runnerId: r.id });
    } catch (err) {
      console.error("admitRunner:", err);
      admitBtn.disabled = false;
    }
  };
  card.appendChild(admitBtn);

  const rejectBtn = document.createElement("button");
  rejectBtn.className =
    "rounded-md border border-zinc-700 bg-zinc-800/60 px-3 py-1.5 text-sm font-medium text-zinc-200 hover:bg-zinc-800 disabled:cursor-progress disabled:opacity-60 light:border-zinc-300 light:bg-zinc-100 light:text-zinc-700 light:hover:bg-zinc-200";
  rejectBtn.textContent = "Reject";
  rejectBtn.onclick = async () => {
    rejectBtn.disabled = true;
    try {
      await client.rejectRunner({ runnerId: r.id });
    } catch (err) {
      console.error("rejectRunner:", err);
      rejectBtn.disabled = false;
    }
  };
  card.appendChild(rejectBtn);

  return card;
}

function rosterRow(r: Runner): HTMLElement {
  const card = document.createElement("div");
  card.className =
    "flex items-center gap-3 rounded-lg border border-zinc-800 bg-zinc-900/40 px-4 py-3 light:border-zinc-200 light:bg-white";

  const meta = document.createElement("div");
  meta.className = "min-w-0 flex-1";
  const top = document.createElement("div");
  top.className = "flex items-center gap-2";
  const name = document.createElement("div");
  name.className = "font-semibold";
  name.textContent = r.name;
  top.appendChild(name);
  const badge = statusBadge(r.status);
  const badgeEl = document.createElement("span");
  badgeEl.className = `rounded-md px-2 py-0.5 text-xs font-medium ${badge.cls}`;
  badgeEl.textContent = badge.text;
  top.appendChild(badgeEl);
  meta.appendChild(top);

  const sub = document.createElement("div");
  sub.className = "text-xs text-zinc-400 light:text-zinc-500";
  if (r.status === RunnerStatus.BUSY && r.currentAssignment) {
    const a = r.currentAssignment;
    sub.textContent = `Problem ${a.problemLabel} → ${a.teamName} · busy ${fmtAgo(a.assignedAt)}`;
  } else if (r.status === RunnerStatus.AVAILABLE && r.availableSince) {
    sub.textContent = `available ${fmtAgo(r.availableSince)}`;
  } else {
    sub.textContent = `last seen ${fmtAgo(r.lastSeenAt)}`;
  }
  meta.appendChild(sub);
  card.appendChild(meta);

  if (r.status === RunnerStatus.BUSY && r.currentAssignment) {
    const forceBtn = document.createElement("button");
    forceBtn.className =
      "rounded-md border border-amber-700/50 bg-amber-900/30 px-3 py-1.5 text-xs font-medium text-amber-300 hover:bg-amber-900/60 disabled:cursor-progress disabled:opacity-60 light:border-amber-300 light:bg-amber-100 light:text-amber-800 light:hover:bg-amber-200";
    forceBtn.textContent = "Force return";
    forceBtn.title = "Cancel this assignment and put the balloon back in the queue.";
    const assignmentId = r.currentAssignment.id;
    forceBtn.onclick = async () => {
      forceBtn.disabled = true;
      try {
        await client.forceReturnAssignment({ assignmentId });
      } catch (err) {
        console.error("forceReturnAssignment:", err);
        forceBtn.disabled = false;
      }
    };
    card.appendChild(forceBtn);
  }

  const kickBtn = document.createElement("button");
  kickBtn.className =
    "rounded-md border border-zinc-700 bg-zinc-800/60 px-3 py-1.5 text-xs font-medium text-zinc-300 hover:bg-zinc-800 disabled:cursor-progress disabled:opacity-60 light:border-zinc-300 light:bg-zinc-100 light:text-zinc-700 light:hover:bg-zinc-200";
  kickBtn.textContent = "Kick";
  kickBtn.title = "End this runner's session.";
  kickBtn.onclick = async () => {
    if (!confirm(`Kick ${r.name}?`)) return;
    kickBtn.disabled = true;
    try {
      await client.kickRunner({ runnerId: r.id });
    } catch (err) {
      console.error("kickRunner:", err);
      kickBtn.disabled = false;
    }
  };
  card.appendChild(kickBtn);
  return card;
}

function renderRunners() {
  const all = [...runners.values()].filter(
    (r) => r.status !== RunnerStatus.OFFLINE && r.status !== RunnerStatus.REJECTED,
  );
  const pendingAdmits = all
    .filter((r) => r.status === RunnerStatus.PENDING_ADMIT)
    .sort((a, b) => Number(a.id) - Number(b.id));
  const roster = all
    .filter((r) => r.status !== RunnerStatus.PENDING_ADMIT)
    .sort((a, b) => Number(a.id) - Number(b.id));

  pendingAdmitCountEl.textContent = `(${pendingAdmits.length})`;
  pendingAdmitSection.classList.toggle("hidden", pendingAdmits.length === 0);
  pendingAdmitEl.innerHTML = "";
  for (const r of pendingAdmits) pendingAdmitEl.appendChild(admitRow(r));

  rosterCountEl.textContent = `(${roster.length})`;
  rosterEl.innerHTML = "";
  for (const r of roster) rosterEl.appendChild(rosterRow(r));
  rosterEmptyEl.classList.toggle("hidden", roster.length !== 0);
}

async function streamRunners() {
  for await (const ev of client.streamRunners({})) {
    if (ev.kind === StreamRunnersResponse_Kind.SNAPSHOT) {
      runners.clear();
      for (const r of ev.snapshot) runners.set(r.id.toString(), r);
    } else if (ev.kind === StreamRunnersResponse_Kind.UPSERT && ev.runner) {
      runners.set(ev.runner.id.toString(), ev.runner);
    } else if (ev.kind === StreamRunnersResponse_Kind.REMOVED && ev.runner) {
      runners.delete(ev.runner.id.toString());
    }
    renderRunners();
  }
}

async function runRosterForever() {
  let backoff = 1000;
  while (true) {
    try {
      await streamRunners();
      backoff = 1000;
    } catch (err) {
      console.error("roster stream error:", err);
      await new Promise((r) => setTimeout(r, backoff));
      backoff = Math.min(backoff * 2, 15000);
    }
  }
}

// Keep the "busy 3m ago" relative timestamps ticking.
window.setInterval(() => {
  if (runners.size > 0) renderRunners();
}, 5000);

runForever();
runRosterForever();
