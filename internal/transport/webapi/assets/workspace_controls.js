"use strict";

const SCAN_POLL_MS = 2000;
const SCAN_POLL_LIMIT = 150;

function workspaceRow(title, detail, meta, actions = []) {
  const card = document.createElement("article");
  card.className = "result-card";
  const strong = document.createElement("strong"); strong.textContent = title;
  const body = document.createElement("small"); body.textContent = detail;
  card.append(strong, body);
  if (meta) { const extra = document.createElement("small"); extra.textContent = meta; card.append(extra); }
  if (actions.length) {
    const row = document.createElement("div");
    row.className = "card-actions";
    row.append(...actions);
    card.append(row);
  }
  return card;
}

async function loadProjectList() {
  const container = byId("project-list");
  if (!container) return;
  try {
    const projects = await api("/api/v1/projects?limit=200");
    container.replaceChildren(...(projects.length
      ? projects.map(project => {
        const select = document.createElement("button");
        select.type = "button";
        select.className = "quiet";
        select.textContent = "Select";
        select.addEventListener("click", () => {
          byId("project-id").value = project.id;
          byId("project-id").dispatchEvent(new Event("change"));
          showMessage(`Project ${project.name} selected.`, true);
        });
        return workspaceRow(project.name, project.description || "No description",
          `ID ${project.id} · created ${project.createdAt.slice(0, 10)}`, [select]);
      })
      : [workspaceRow("No projects yet", "Create one below to start a catalog.", "")]));
  } catch (error) { showMessage(error.message); }
}

async function loadDataSourceList() {
  const container = byId("data-source-list");
  if (!container) return;
  let project;
  try { project = requireProject(); } catch { container.replaceChildren(workspaceRow("Select a project", "Pick a project in the header to see its data sources.", "")); return; }
  try {
    const sources = await api(`/api/v1/projects/${project}/data-sources?limit=200`);
    container.replaceChildren(...(sources.length
      ? sources.map(source => {
        const scan = document.createElement("button");
        scan.type = "button";
        scan.textContent = "Start full scan";
        const status = document.createElement("small");
        scan.addEventListener("click", () => startScan(project, source.id, status, scan));
        const card = workspaceRow(source.name, source.dsnEnvironment ? `DSN variable ${source.dsnEnvironment}` : "MySQL",
          `ID ${source.id}`, [scan]);
        card.append(status);
        return card;
      })
      : [workspaceRow("No data sources yet", "Add one below, then run a schema scan.", "")]));
  } catch (error) { showMessage(error.message); }
}

async function loadRepositoryList() {
  const container = byId("repository-list");
  if (!container || state.role !== "ADMIN") return;
  let project;
  try { project = requireProject(); } catch { return; }
  try {
    const repositories = await api(`/api/v1/projects/${project}/repositories?limit=200`);
    container.replaceChildren(...(repositories.length
      ? repositories.map(repository => workspaceRow(repository.name, `Default branch ${repository.defaultBranch}`, `ID ${repository.id}`))
      : [workspaceRow("No repositories yet", "Register one so Agent evidence can cite it.", "")]));
  } catch (error) { showMessage(error.message); }
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
      if (job.status === "SUCCEEDED" || job.status === "FAILED") return;
    } catch { statusLine.textContent = `Job ${jobId} · checking again…`; }
    await new Promise(resolve => setTimeout(resolve, SCAN_POLL_MS));
  }
  statusLine.textContent = `Job ${jobId} · still running. Reload to check again.`;
}

function initializeWorkspace() {
  loadProjectList();
  const select = byId("project-id");
  if (select) select.addEventListener("change", () => { loadDataSourceList(); loadRepositoryList(); });
  document.querySelectorAll('[data-panel-link="projects"]').forEach(link => link.addEventListener("click", loadProjectList));
  document.querySelectorAll('[data-panel-link="data-sources"]').forEach(link => link.addEventListener("click", () => {
    loadDataSourceList(); loadRepositoryList();
  }));
}
