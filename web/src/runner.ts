import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import {
  BalloonService,
  RunnerStatus,
} from "./gen/balloons/v1/balloons_pb.js";
import type {
  Runner,
  Assignment,
  WatchRunnerStateResponse,
} from "./gen/balloons/v1/balloons_pb.js";

const transport = createConnectTransport({ baseUrl: window.location.origin });
const client = createClient(BalloonService, transport);

// --- DOM handles ---
const statusDot = document.getElementById("status-dot")!;
const statusText = document.getElementById("status-text")!;
const noteEl = document.getElementById("note")!;

const viewName = document.getElementById("view-name")!;
const nameForm = document.getElementById("name-form") as HTMLFormElement;
const nameInput = document.getElementById("name-input") as HTMLInputElement;
const nameSubmit = document.getElementById("name-submit") as HTMLButtonElement;
const nameError = document.getElementById("name-error")!;

const viewWait = document.getElementById("view-wait")!;
const waitIcon = document.getElementById("wait-icon")!;
const waitTitle = document.getElementById("wait-title")!;
const waitSub = document.getElementById("wait-sub")!;
const waitPrimary = document.getElementById("wait-primary") as HTMLButtonElement;
const waitSecondary = document.getElementById("wait-secondary") as HTMLButtonElement;

const viewBusy = document.getElementById("view-busy")!;
const firstSolveBanner = document.getElementById("first-solve-banner")!;
const busyBall = document.getElementById("busy-ball")!;
const busyProblem = document.getElementById("busy-problem")!;
const busyTeam = document.getElementById("busy-team")!;
const busyLocation = document.getElementById("busy-location")!;
const busyDeliver = document.getElementById("busy-deliver") as HTMLButtonElement;

const viewRejected = document.getElementById("view-rejected")!;
const rejectedRestart = document.getElementById("rejected-restart") as HTMLButtonElement;

// --- View switching ---
type View = "name" | "wait" | "busy" | "rejected";
function showView(v: View) {
  viewName.classList.toggle("hidden", v !== "name");
  viewName.classList.toggle("flex", v === "name");
  viewWait.classList.toggle("hidden", v !== "wait");
  viewWait.classList.toggle("flex", v === "wait");
  viewBusy.classList.toggle("hidden", v !== "busy");
  viewBusy.classList.toggle("flex", v === "busy");
  viewRejected.classList.toggle("hidden", v !== "rejected");
  viewRejected.classList.toggle("flex", v === "rejected");
}

type ConnStatus = "connecting" | "connected" | "disconnected";
function setStatus(s: ConnStatus) {
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
    disconnected: "Reconnecting…",
  }[s];
}

function showNote(text: string) {
  if (!text) {
    noteEl.classList.add("hidden");
    noteEl.textContent = "";
    return;
  }
  noteEl.textContent = text;
  noteEl.classList.remove("hidden");
  window.setTimeout(() => {
    if (noteEl.textContent === text) {
      noteEl.classList.add("hidden");
    }
  }, 6000);
}

// --- Name entry ---
nameForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  const name = nameInput.value.trim();
  if (!name) return;
  nameSubmit.disabled = true;
  nameSubmit.textContent = "Requesting…";
  nameError.classList.add("hidden");
  try {
    await client.requestRunnerSession({ name });
    streamForever();
  } catch (err) {
    console.error("requestRunnerSession:", err);
    nameError.textContent = String((err as Error).message || err);
    nameError.classList.remove("hidden");
  } finally {
    nameSubmit.disabled = false;
    nameSubmit.textContent = "Request session";
  }
});

// --- Action buttons ---
async function setAvailable(available: boolean) {
  try {
    await client.setRunnerAvailable({ available });
  } catch (err) {
    console.error("setRunnerAvailable:", err);
    showNote("Couldn't update availability — try again.");
  }
}

async function completeAssignment(assignmentId: bigint) {
  busyDeliver.disabled = true;
  busyDeliver.textContent = "Marking…";
  try {
    await client.completeAssignment({ assignmentId });
  } catch (err) {
    console.error("completeAssignment:", err);
    showNote("Couldn't mark delivered — try again.");
    busyDeliver.disabled = false;
    busyDeliver.textContent = "✅ Delivered";
  }
}

async function readyForNext() {
  try {
    await client.readyForNext({});
  } catch (err) {
    console.error("readyForNext:", err);
    showNote("Couldn't request next balloon — try again.");
  }
}

// --- Render runner state ---
let currentAssignmentId: bigint | null = null;

type WaitButton = { text: string; onClick: () => void };
type WaitConfig = {
  icon: string;
  title: string;
  sub: string;
  primary?: WaitButton;
  secondary?: WaitButton;
};

const waitConfigs: Partial<Record<RunnerStatus, WaitConfig>> = {
  [RunnerStatus.PENDING_ADMIT]: {
    icon: "⌛",
    title: "Waiting for admin",
    sub: "An admin will admit you from the control panel.",
  },
  [RunnerStatus.IDLE]: {
    icon: "☕",
    title: "On break",
    sub: "Tap below when you're ready to deliver balloons.",
    primary: { text: "I'm available", onClick: () => setAvailable(true) },
  },
  [RunnerStatus.AVAILABLE]: {
    icon: "🎈",
    title: "Waiting for a balloon",
    sub: "You're at the top of the queue.",
    secondary: { text: "Take a break", onClick: () => setAvailable(false) },
  },
  [RunnerStatus.DELIVERED_PENDING_ACK]: {
    icon: "🎉",
    title: "Delivered!",
    sub: "Take a breath. Ready for the next?",
    primary: { text: "Ready for next", onClick: () => readyForNext() },
    secondary: { text: "Take a break", onClick: () => setAvailable(false) },
  },
};

function render(ev: WatchRunnerStateResponse) {
  if (ev.note) showNote(ev.note);
  if (!ev.runner) return;
  const r = ev.runner;
  if (r.status === RunnerStatus.BUSY) {
    renderBusy(r);
    return;
  }
  // Treat OFFLINE like REJECTED for UX — admin kicked them.
  if (r.status === RunnerStatus.REJECTED || r.status === RunnerStatus.OFFLINE) {
    showView("rejected");
    return;
  }
  const cfg = waitConfigs[r.status];
  if (cfg) renderWait(cfg);
}

function renderWait(cfg: WaitConfig) {
  showView("wait");
  waitIcon.textContent = cfg.icon;
  waitTitle.textContent = cfg.title;
  waitSub.textContent = cfg.sub;
  configureButton(waitPrimary, cfg.primary);
  configureButton(waitSecondary, cfg.secondary);
}

function configureButton(btn: HTMLButtonElement, cfg: WaitButton | undefined) {
  if (!cfg) {
    btn.classList.add("hidden");
    btn.onclick = null;
    return;
  }
  btn.classList.remove("hidden");
  btn.textContent = cfg.text;
  btn.onclick = cfg.onClick;
}

function renderBusy(r: Runner) {
  const a: Assignment | undefined = r.currentAssignment;
  if (!a) {
    // Server says busy but no assignment — rare race. Fall back to wait.
    renderWait(waitConfigs[RunnerStatus.AVAILABLE]!);
    return;
  }
  showView("busy");
  currentAssignmentId = a.id;
  busyBall.style.background = a.problemRgb || "#888";
  busyBall.textContent = a.problemLabel || "?";
  busyProblem.textContent = "Problem " + (a.problemLabel || "?");
  busyTeam.textContent = a.teamName || "";
  busyLocation.textContent = a.teamLocation || "—";
  firstSolveBanner.classList.toggle("hidden", !a.firstSolve);
  busyDeliver.disabled = false;
  busyDeliver.textContent = "✅ Delivered";
  busyDeliver.onclick = () => {
    if (currentAssignmentId !== null) completeAssignment(currentAssignmentId);
  };
}

// --- Stream loop ---
async function stream() {
  for await (const ev of client.watchRunnerState({})) {
    setStatus("connected");
    render(ev);
  }
}

async function streamForever() {
  let backoff = 1000;
  while (true) {
    setStatus("connecting");
    try {
      await stream();
      backoff = 1000;
    } catch (err) {
      // The server clears runner_session on Unauthenticated / PermissionDenied
      // errors, so looping reconnects against a token that will never
      // authenticate again is pointless. Drop to the name-entry view instead.
      if (
        err instanceof ConnectError &&
        (err.code === Code.Unauthenticated || err.code === Code.PermissionDenied)
      ) {
        setStatus("disconnected");
        showView("name");
        return;
      }
      console.error("watchRunnerState:", err);
      setStatus("disconnected");
      await new Promise((r) => setTimeout(r, backoff));
      backoff = Math.min(backoff * 2, 15000);
    }
  }
}

rejectedRestart.onclick = () => {
  showView("name");
  nameInput.value = "";
  nameInput.focus();
};

// On load: try to resume an existing session. If WatchRunnerState 401s we
// drop to the name entry view.
showView("wait");
waitTitle.textContent = "Connecting…";
waitSub.textContent = "";
waitPrimary.classList.add("hidden");
waitSecondary.classList.add("hidden");
streamForever();
