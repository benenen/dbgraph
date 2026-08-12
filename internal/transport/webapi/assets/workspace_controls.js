"use strict";

// GET .../schema-scan-jobs/{jobID} sits behind limitExpensiveRead (30
// requests/minute per principal and per IP; internal/transport/webapi/rate_limit.go).
// Keep the poll interval comfortably under that budget so one running scan
// doesn't consume it and starve node search, project list, proposals, and
// audit for the rest of the minute. SCAN_POLL_LIMIT is sized so the overall
// timeout stays at five minutes: 5000ms * 60 = 300000ms.
const SCAN_POLL_MS = 5000;
const SCAN_POLL_LIMIT = 60;
// Terminal job statuses per internal/jobs/jobs.go (StatusSucceeded, StatusFailed,
// StatusCancelled) as named by jobStatusName in internal/transport/webapi/read_api.go.
// PENDING and RUNNING are the only non-terminal values that enum can emit.
const TERMINAL_JOB_STATUSES = new Set(["SUCCEEDED", "FAILED", "CANCELLED"]);

async function loadProjectList() {
  const container = byId("project-list");
  if (!container) return;
  try {
    const projects = await api("/api/v1/projects?limit=200");
    replaceCards(container, projects.map(project => {
      const select = document.createElement("button");
      select.type = "button";
      select.className = "quiet";
      select.textContent = "Select";
      select.addEventListener("click", () => {
        byId("project-id").value = project.id;
        byId("project-id").dispatchEvent(new Event("change"));
        showMessage(`Project ${project.name} selected.`, true);
      });
      const card = resultCard(project.name, project.description || "No description",
        `ID ${project.id} · created ${project.createdAt.slice(0, 10)}`);
      const actions = document.createElement("div");
      actions.className = "card-actions";
      actions.append(select);
      card.append(actions);
      return card;
    }), "No projects yet");
  } catch (error) { showMessage(error.message); }
}

let dataSourceListRequestToken = 0;

async function loadDataSourceList() {
  const container = byId("data-source-list");
  if (!container) return;
  const requestToken = ++dataSourceListRequestToken;
  let project;
  try { project = requireProject(); }
  catch { container.replaceChildren(resultCard("Select a project", "Pick a project in the header to see its data sources.")); return; }
  try {
    const sources = await api(`/api/v1/projects/${project}/data-sources?limit=200`);
    if (requestToken !== dataSourceListRequestToken) return; // a newer call already superseded this one
    replaceCards(container, sources.map(source => {
      const scan = document.createElement("button");
      scan.type = "button";
      scan.textContent = "Start full scan";
      const status = document.createElement("small");
      scan.addEventListener("click", () => startScan(project, source.id, status, scan));
      const card = resultCard(source.name, source.dsnEnvironment ? `DSN variable ${source.dsnEnvironment}` : "MySQL",
        `ID ${source.id}`);
      const actions = document.createElement("div");
      actions.className = "card-actions";
      actions.append(scan);
      card.append(actions, status);
      return card;
    }), "No data sources yet");
  } catch (error) { if (requestToken === dataSourceListRequestToken) showMessage(error.message); }
}

let repositoryListRequestToken = 0;

async function loadRepositoryList() {
  const container = byId("repository-list");
  if (!container || state.role !== "ADMIN") return;
  const requestToken = ++repositoryListRequestToken;
  let project;
  try { project = requireProject(); }
  catch { container.replaceChildren(resultCard("Select a project", "Pick a project in the header to see its repositories.")); return; }
  try {
    const repositories = await api(`/api/v1/projects/${project}/repositories?limit=200`);
    if (requestToken !== repositoryListRequestToken) return; // a newer call already superseded this one
    replaceCards(container, repositories.map(repository =>
      resultCard(repository.name, `Default branch ${repository.defaultBranch}`, `ID ${repository.id}`)
    ), "No repositories yet");
  } catch (error) { if (requestToken === repositoryListRequestToken) showMessage(error.message); }
}

async function startScan(projectId, dataSourceId, statusLine, trigger) {
  trigger.disabled = true;
  statusLine.textContent = "Queueing scan…";
  try {
    const job = await api(`/api/v1/projects/${projectId}/data-sources/${dataSourceId}/schema-scan-jobs`, {
      method: "POST", body: JSON.stringify({ mode: "FULL", reason: "Full inventory from the Data sources panel" }),
    });
    await pollScanJob(projectId, job.id, statusLine);
  } catch (error) { statusLine.textContent = error.message; }
  finally { trigger.disabled = false; }
}

async function pollScanJob(projectId, jobId, statusLine) {
  for (let attempt = 0; attempt < SCAN_POLL_LIMIT; attempt += 1) {
    try {
      const job = await api(`/api/v1/projects/${projectId}/schema-scan-jobs/${jobId}`);
      statusLine.textContent = `Job ${jobId} · ${job.status}${job.errorCode ? ` · ${job.errorCode}` : ""}`;
      if (TERMINAL_JOB_STATUSES.has(job.status)) return;
    } catch (error) {
      // A 429 here means the read budget is exhausted — that's worth telling
      // the operator about, not hiding behind a generic "checking again".
      statusLine.textContent = error.status === 429
        ? `Job ${jobId} · rate limited (HTTP 429); waiting to check again…`
        : `Job ${jobId} · checking again…`;
    }
    await new Promise(resolve => setTimeout(resolve, SCAN_POLL_MS));
  }
  statusLine.textContent = `Job ${jobId} · still running. Reload to check again.`;
}

function initializeWorkspace() {
  // Projects has no hash-navigation gap: this eager call runs on every page
  // load regardless of which panel is active, so #projects is always
  // populated by the time the panel is visible. Data sources needs its own
  // per-project load, which activatePanel() in app.js now covers for click,
  // initial location.hash, and hashchange — a separate click listener here
  // would double-fetch the same lists on every click, the class of bug fixed
  // for Schema Explorer.
  loadProjectList();
  const select = byId("project-id");
  if (select) select.addEventListener("change", () => { loadDataSourceList(); loadRepositoryList(); });
  document.querySelectorAll('[data-panel-link="projects"]').forEach(link => link.addEventListener("click", loadProjectList));
}
