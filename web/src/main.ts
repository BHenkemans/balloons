import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import {
  BalloonService,
  StreamBalloonsResponse_Kind,
} from "./gen/balloons/v1/balloons_pb.js";
import type { Balloon } from "./gen/balloons/v1/balloons_pb.js";
import { row } from "./render.js";
import { initScan } from "./scan.js";

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

function render() {
  const all = [...state.values()];
  const byNewest = (a: Balloon, b: Balloon) => Number(b.id) - Number(a.id);
  const pending = all.filter((b) => !b.done).sort(byNewest);
  const delivered = all.filter((b) => b.done).sort(byNewest);

  pendingCountEl.textContent = `(${pending.length})`;
  deliveredCountEl.textContent = `(${delivered.length})`;

  pendingEl.innerHTML = "";
  for (const b of pending) pendingEl.appendChild(row(b, client));
  pendingEmptyEl.classList.toggle("hidden", pending.length !== 0);

  deliveredEl.innerHTML = "";
  for (const b of delivered) deliveredEl.appendChild(row(b, client));
  deliveredEmptyEl.classList.toggle("hidden", delivered.length !== 0);
}

initScan({ state, client, scanForm, scanInput, scanHint, toastStack });

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
