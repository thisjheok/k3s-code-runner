const healthStatus = document.querySelector("#healthStatus");
const runForm = document.querySelector("#runForm");
const runButton = document.querySelector("#runButton");
const refreshButton = document.querySelector("#refreshButton");
const message = document.querySelector("#message");
const runsList = document.querySelector("#runsList");
const logs = document.querySelector("#logs");
const nodeDistribution = document.querySelector("#nodeDistribution");
const workerPods = document.querySelector("#workerPods");
const monitorUpdated = document.querySelector("#monitorUpdated");
const metricQueueDepth = document.querySelector("#metricQueueDepth");
const metricWorkerReplicas = document.querySelector("#metricWorkerReplicas");
const metricJobCount = document.querySelector("#metricJobCount");
const metricRunnerPodCount = document.querySelector("#metricRunnerPodCount");
const historyChart = document.querySelector("#historyChart");
const queuedRuns = document.querySelector("#queuedRuns");
const workerPodList = document.querySelector("#workerPodList");
const nodeLoadList = document.querySelector("#nodeLoadList");
const runnerPodRows = document.querySelector("#runnerPodRows");

const statusFields = {
  queueDepth: document.querySelector("#statusQueueDepth"),
  workerReplicas: document.querySelector("#statusWorkerReplicas"),
  runId: document.querySelector("#statusRunId"),
  status: document.querySelector("#statusValue"),
  jobName: document.querySelector("#statusJobName"),
  podName: document.querySelector("#statusPodName"),
  nodeName: document.querySelector("#statusNodeName"),
  reason: document.querySelector("#statusReason"),
};

let selectedRunId = "";
let activePollRunId = "";
const history = [];
const maxHistoryPoints = 60;

const terminalStatuses = new Set(["Succeeded", "Failed", "Timeout", "OOMKilled"]);

async function fetchJSON(path, options = {}) {
  const response = await fetch(path, {
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
    ...options,
  });

  const text = await response.text();
  let data = {};

  try {
    data = text ? JSON.parse(text) : {};
  } catch (error) {
    data = { error: text || "The server returned an unreadable response." };
  }

  if (!response.ok) {
    throw new Error(data.error || `Request failed with ${response.status}`);
  }
  return data;
}

async function deleteJSON(path) {
  const response = await fetch(path, { method: "DELETE" });
  const text = await response.text();
  let data = {};

  try {
    data = text ? JSON.parse(text) : {};
  } catch (error) {
    data = { error: text || "The server returned an unreadable response." };
  }

  if (!response.ok) {
    throw new Error(data.error || `Request failed with ${response.status}`);
  }
}

function setMessage(text, isError = false) {
  message.textContent = text;
  message.classList.toggle("error", isError);
}

function setStatus(run = {}) {
  statusFields.runId.textContent = run.run_id || "-";
  statusFields.status.textContent = run.status || "-";
  statusFields.jobName.textContent = run.job_name || "-";
  statusFields.podName.textContent = run.pod_name || "-";
  statusFields.nodeName.textContent = run.node_name || "-";
  statusFields.reason.textContent = run.reason || "-";
}

function setSystemStatus(status = {}) {
  statusFields.queueDepth.textContent = status.queue_depth ?? "-";
  const replicas = status.worker_replicas ?? "-";
  const readyReplicas = status.worker_ready_replicas ?? "-";
  statusFields.workerReplicas.textContent = `${readyReplicas}/${replicas}`;
  renderMonitor(status);

  const nodeRuns = status.node_runs || {};
  const nodeEntries = Object.entries(nodeRuns).sort(([a], [b]) => a.localeCompare(b));
  nodeDistribution.innerHTML = "";
  if (nodeEntries.length === 0) {
    const empty = document.createElement("div");
    empty.className = "empty-line";
    empty.textContent = "No runner pods yet.";
    nodeDistribution.append(empty);
  } else {
    for (const [nodeName, count] of nodeEntries) {
      const row = document.createElement("div");
      row.className = "distribution-row";
      const name = document.createElement("span");
      name.textContent = nodeName;
      const value = document.createElement("strong");
      value.textContent = String(count);
      row.append(name, value);
      nodeDistribution.append(row);
    }
  }

  workerPods.innerHTML = "";
  for (const pod of status.worker_pods || []) {
    const row = document.createElement("div");
    row.className = "worker-pod";
    row.textContent = `${pod.name} | ${pod.phase}${pod.ready ? " | ready" : ""} | ${pod.node_name || "pending"}`;
    workerPods.append(row);
  }
}

function renderMonitor(status = {}) {
  const queueDepth = Number(status.queue_depth || 0);
  const workerReplicas = Number(status.worker_replicas || 0);
  const workerReadyReplicas = Number(status.worker_ready_replicas || 0);
  const runnerJobs = status.runner_jobs || [];
  const runnerPods = status.runner_pods || [];
  const workerPodItems = status.worker_pods || [];

  metricQueueDepth.textContent = String(queueDepth);
  metricWorkerReplicas.textContent = `${workerReadyReplicas}/${workerReplicas}`;
  metricJobCount.textContent = String(runnerJobs.length);
  metricRunnerPodCount.textContent = String(runnerPods.length);
  monitorUpdated.textContent = new Date().toLocaleTimeString();

  history.push({
    queueDepth,
    workerReplicas,
    at: Date.now(),
  });
  if (history.length > maxHistoryPoints) {
    history.shift();
  }
  renderHistory();
  renderQueuedRuns(status.queued_run_ids || []);
  renderWorkerPodList(workerPodItems);
  renderNodeLoad(status.node_runs || {});
  renderRunnerRows(runnerPods, runnerJobs);
}

function renderHistory() {
  historyChart.innerHTML = "";
  const maxQueue = Math.max(1, ...history.map((point) => point.queueDepth));
  const maxReplicas = Math.max(1, ...history.map((point) => point.workerReplicas));

  for (const point of history) {
    const bar = document.createElement("div");
    bar.className = "history-point";
    bar.title = `queue ${point.queueDepth}, workers ${point.workerReplicas}`;

    const queueBar = document.createElement("span");
    queueBar.className = "queue-bar";
    queueBar.style.height = `${Math.max(4, (point.queueDepth / maxQueue) * 100)}%`;

    const replicaBar = document.createElement("span");
    replicaBar.className = "replica-bar";
    replicaBar.style.height = `${Math.max(4, (point.workerReplicas / maxReplicas) * 100)}%`;

    bar.append(queueBar, replicaBar);
    historyChart.append(bar);
  }
}

function renderQueuedRuns(runIds) {
  queuedRuns.innerHTML = "";
  if (runIds.length === 0) {
    queuedRuns.append(emptyLine("Queue is empty."));
    return;
  }
  for (const runId of runIds) {
    const row = document.createElement("div");
    row.className = "compact-row";
    row.textContent = runId;
    queuedRuns.append(row);
  }
}

function renderWorkerPodList(pods) {
  workerPodList.innerHTML = "";
  if (pods.length === 0) {
    workerPodList.append(emptyLine("No worker pods."));
    return;
  }
  for (const pod of pods) {
    const row = document.createElement("div");
    row.className = "compact-row";
    row.textContent = `${pod.name} | ${pod.phase}${pod.ready ? " | ready" : ""} | ${pod.node_name || "pending"}`;
    workerPodList.append(row);
  }
}

function renderNodeLoad(nodeRuns) {
  nodeLoadList.innerHTML = "";
  const entries = Object.entries(nodeRuns).sort(([a], [b]) => a.localeCompare(b));
  if (entries.length === 0) {
    nodeLoadList.append(emptyLine("No runner pods on nodes."));
    return;
  }

  const maxCount = Math.max(1, ...entries.map(([, count]) => Number(count || 0)));
  for (const [nodeName, count] of entries) {
    const row = document.createElement("div");
    row.className = "node-load-row";

    const label = document.createElement("div");
    label.className = "node-load-label";

    const name = document.createElement("span");
    name.textContent = nodeName;

    const value = document.createElement("strong");
    value.textContent = `${count} pod${count === 1 ? "" : "s"}`;

    const track = document.createElement("div");
    track.className = "node-load-track";

    const bar = document.createElement("span");
    bar.style.width = `${Math.max(4, (Number(count || 0) / maxCount) * 100)}%`;

    label.append(name, value);
    track.append(bar);
    row.append(label, track);
    nodeLoadList.append(row);
  }
}

function renderRunnerRows(pods, jobs) {
  runnerPodRows.innerHTML = "";
  const jobsByRunID = new Map(jobs.map((job) => [job.run_id, job]));

  if (pods.length === 0 && jobs.length === 0) {
    const row = document.createElement("tr");
    const cell = document.createElement("td");
    cell.colSpan = 5;
    cell.textContent = "No runner pods or jobs yet.";
    row.append(cell);
    runnerPodRows.append(row);
    return;
  }

  const rows = pods.length > 0 ? pods : jobs.map((job) => ({ run_id: job.run_id, job_name: job.name, phase: job.status }));
  for (const pod of rows) {
    const job = jobsByRunID.get(pod.run_id) || {};
    const row = document.createElement("tr");
    const values = [
      pod.run_id || "-",
      pod.phase || job.status || "-",
      pod.name || "-",
      pod.node_name || "pending",
      pod.job_name || job.name || "-",
    ];
    for (const value of values) {
      const cell = document.createElement("td");
      cell.textContent = value;
      row.append(cell);
    }
    runnerPodRows.append(row);
  }
}

function emptyLine(text) {
  const line = document.createElement("div");
  line.className = "empty-line";
  line.textContent = text;
  return line;
}

function showError(error, context) {
  const detail = error instanceof Error ? error.message : String(error);
  const messageText = `${context}: ${detail}`;
  setMessage(messageText, true);
  statusFields.status.textContent = "Error";
  statusFields.reason.textContent = detail;
  logs.textContent = messageText;
}

function sleep(ms) {
  return new Promise((resolve) => {
    window.setTimeout(resolve, ms);
  });
}

async function checkHealth() {
  try {
    await fetchJSON("/health");
    healthStatus.textContent = "Healthy";
    healthStatus.className = "health ok";
  } catch (error) {
    healthStatus.textContent = "Offline";
    healthStatus.className = "health bad";
  }
}

function statusClass(status) {
  return String(status || "").toLowerCase();
}

function renderRuns(runs) {
  runsList.innerHTML = "";

  if (runs.length === 0) {
    const empty = document.createElement("div");
    empty.className = "run-item";
    empty.textContent = "No recent runs.";
    runsList.append(empty);
    return;
  }

  for (const run of runs) {
    const item = document.createElement("div");
    item.className = `run-item${run.run_id === selectedRunId ? " active" : ""}`;

    const runId = document.createElement("span");
    runId.className = "run-id";
    runId.textContent = run.run_id || "-";

    const details = document.createElement("div");
    details.className = "run-details";

    const statusRow = document.createElement("span");
    statusRow.className = "run-detail";

    const status = document.createElement("span");
    status.className = `status-text ${statusClass(run.status)}`;
    status.textContent = run.status || "-";

    const podName = document.createElement("span");
    podName.className = "run-detail important-detail";
    podName.textContent = `pod_name: ${run.pod_name || "-"}`;

    const nodeName = document.createElement("span");
    nodeName.className = "run-detail important-detail";
    nodeName.textContent = `node_name: ${run.node_name || "-"}`;

    const reason = document.createElement("span");
    reason.className = "run-detail important-detail";
    reason.textContent = `reason: ${run.reason || "-"}`;

    const actions = document.createElement("div");
    actions.className = "run-actions";

    const logsButton = document.createElement("button");
    logsButton.type = "button";
    logsButton.className = "mini-button";
    logsButton.textContent = "Logs";
    logsButton.addEventListener("click", () => showRunLogs(run.run_id));

    const deleteButton = document.createElement("button");
    deleteButton.type = "button";
    deleteButton.className = "mini-button danger-button";
    deleteButton.textContent = "Delete";
    deleteButton.addEventListener("click", () => deleteRun(run.run_id));

    statusRow.append("status: ", status);
    details.append(statusRow, podName, nodeName, reason);
    actions.append(logsButton, deleteButton);
    item.append(runId, details, actions);
    runsList.append(item);
  }
}

async function loadRuns() {
  const data = await fetchJSON("/runs");
  const runs = data.runs || [];
  renderRuns(runs);
  return runs;
}

async function loadSystemStatus() {
  const status = await fetchJSON("/system/status");
  setSystemStatus(status);
  return status;
}

async function loadRunDetail(runId) {
  const run = await fetchJSON(`/runs/${encodeURIComponent(runId)}`);
  setStatus(run);
  return run;
}

async function loadRunLogs(runId) {
  const data = await fetchJSON(`/runs/${encodeURIComponent(runId)}/logs`);
  logs.textContent = data.logs || "No logs available yet.";
}

async function showRunLogs(runId) {
  if (!runId) {
    return;
  }

  selectedRunId = runId;
  logs.textContent = "Loading logs...";

  try {
    await Promise.all([loadRunDetail(runId), loadRunLogs(runId), loadRuns(), loadSystemStatus()]);
    setMessage(`Loaded logs for ${runId}.`);
  } catch (error) {
    showError(error, `Failed to load logs for ${runId}`);
  }
}

async function deleteRun(runId) {
  if (!runId) {
    return;
  }

  setMessage(`Deleting ${runId}...`);

  try {
    await deleteJSON(`/runs/${encodeURIComponent(runId)}`);
    if (selectedRunId === runId) {
      selectedRunId = "";
      setStatus();
      logs.textContent = `Deleted ${runId}.`;
    }
    await Promise.all([loadRuns(), loadSystemStatus()]);
    setMessage(`Deleted ${runId}.`);
  } catch (error) {
    showError(error, `Failed to delete ${runId}`);
  }
}

async function pollRunUntilComplete(runId) {
  activePollRunId = runId;

  while (activePollRunId === runId) {
    const run = await loadRunDetail(runId);
    await Promise.all([loadRuns(), loadSystemStatus()]);

    if (terminalStatuses.has(run.status)) {
      activePollRunId = "";
      return run;
    }

    setMessage(`${runId} is ${run.status || "running"}...`);
    await sleep(1000);
  }

  throw new Error("Polling was cancelled.");
}

function codeWithSleep(language, code, sleepSeconds) {
  if (!sleepSeconds || sleepSeconds <= 0) {
    return code;
  }

  if (language === "node") {
    return `${code}\n\nAtomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ${sleepSeconds * 1000});\n`;
  }

  return `${code}\n\nimport time\ntime.sleep(${sleepSeconds})\n`;
}

async function createRun(payload) {
  return fetchJSON("/runs", {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

runForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  activePollRunId = "";
  runButton.disabled = true;
  setMessage("Creating run...");
  logs.textContent = "Submitting code...";
  setStatus();

  const language = runForm.language.value;
  const batchCount = Math.max(1, Math.min(100, Number(runForm.batchCount.value) || 1));
  const sleepSeconds = Math.max(0, Math.min(600, Number(runForm.sleepSeconds.value) || 0));
  const payload = {
    language,
    code: codeWithSleep(language, runForm.code.value, sleepSeconds),
    timeout_seconds: Number(runForm.timeout.value) || 60,
  };

  try {
    const createdRuns = [];
    for (let i = 0; i < batchCount; i += 1) {
      setMessage(`Creating run ${i + 1}/${batchCount}...`);
      createdRuns.push(await createRun(payload));
    }

    const created = createdRuns[createdRuns.length - 1];

    selectedRunId = created.run_id;
    setStatus(created);
    setMessage(`Created ${createdRuns.length} run${createdRuns.length === 1 ? "" : "s"}. Watching ${created.run_id}...`);
    logs.textContent = "Runs are queued. Logs will appear when the selected run finishes.";
    await Promise.all([loadRuns(), loadSystemStatus()]);

    const completed = await pollRunUntilComplete(created.run_id);
    setMessage(`${completed.run_id} finished with ${completed.status}.`);
    logs.textContent = "Loading logs...";
    await loadRunLogs(completed.run_id);
    await loadRuns();
  } catch (error) {
    showError(error, "Run failed");
  } finally {
    activePollRunId = "";
    runButton.disabled = false;
  }
});

refreshButton.addEventListener("click", async () => {
  try {
    await Promise.all([loadRuns(), loadSystemStatus()]);
    if (selectedRunId) {
      await loadRunDetail(selectedRunId);
    }
    setMessage("Recent runs refreshed.");
  } catch (error) {
    setMessage(error.message, true);
  }
});

setStatus();
setSystemStatus();
checkHealth();
Promise.all([loadRuns(), loadSystemStatus()]).catch((error) => setMessage(error.message, true));
window.setInterval(() => {
  loadSystemStatus().catch(() => {});
}, 2000);
