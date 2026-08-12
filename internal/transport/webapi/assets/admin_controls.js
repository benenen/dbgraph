"use strict";

function initializeAdmin() {
  byId("project-form").addEventListener("submit", async event => {
    event.preventDefault();
    try {
      const project = await api("/api/v1/projects", { method: "POST", body: JSON.stringify({
        name: byId("project-name").value, description: byId("project-description").value,
        reason: byId("project-reason").value,
      }) });
      await loadProjects(project.id);
      byId("project-id").value = project.id;
      byId("project-id").dispatchEvent(new Event("change"));
      loadProjectList();
      showMessage(`Project ${project.id} created.`, true);
    } catch (error) { showMessage(error.message); }
  });
  byId("repository-form").addEventListener("submit", async event => {
    event.preventDefault();
    try {
      const repository = await api(`/api/v1/projects/${requireProject()}/repositories`, { method: "POST", body: JSON.stringify({
        name: byId("repository-name").value, remoteUrl: byId("repository-url").value,
        defaultBranch: byId("repository-branch").value, reason: byId("repository-reason").value,
      }) });
      byId("repository-result").textContent = JSON.stringify(repository, null, 2);
      loadRepositoryList();
      showMessage(`Repository ${repository.id} is ready for Agent relation-init evidence.`, true);
    } catch (error) { showMessage(error.message); }
  });
  byId("data-source-form").addEventListener("submit", async event => {
    event.preventDefault();
    try {
      const source = await api(`/api/v1/projects/${requireProject()}/data-sources`, { method: "POST", body: JSON.stringify({
        name: byId("data-source-name").value, kind: "MYSQL", dsnEnvironment: byId("data-source-environment").value,
        reason: byId("data-source-reason").value,
      }) });
      byId("scan-data-source-id").value = source.id;
      loadDataSourceList();
      showMessage(`Data source ${source.id} created.`, true);
    } catch (error) { showMessage(error.message); }
  });
  byId("scan-start-form").addEventListener("submit", async event => {
    event.preventDefault();
    try {
      const project = requireProject(); const source = byId("scan-data-source-id").value.trim();
      const mode = byId("scan-mode").value;
      const tables = byId("scan-tables").value.split(/[\n,]/).map(value => value.trim()).filter(Boolean);
      if (mode === "FULL" && tables.length) throw new Error("Full scans cannot include a table scope.");
      if (mode === "INCREMENTAL" && !tables.length) throw new Error("Incremental scans require at least one schema.table entry.");
      const job = await api(`/api/v1/projects/${project}/data-sources/${source}/schema-scan-jobs`, { method: "POST", body: JSON.stringify({
        mode, tables, reason: byId("scan-reason").value,
      }) });
      byId("job-id").value = job.id; byId("scan-result").textContent = JSON.stringify(job, null, 2);
      showMessage(`Schema scan job ${job.id} queued.`, true);
    } catch (error) { showMessage(error.message); }
  });
}

function initializeQueries() {
  byId("load-unresolved").addEventListener("click", async () => {
    try { const data = await api(`/api/v1/projects/${requireProject()}/unresolved-findings?limit=100`);
      replaceCards(byId("unresolved-results"), data.findings.map(item => resultCard(item.type, item.summary, `Finding ${item.id}`)), "No unresolved findings");
    } catch (error) { showMessage(error.message); }
  });
  byId("job-form").addEventListener("submit", event => loadExact(event, "schema-scan-jobs", "job-id", "job-details"));
  byId("init-form").addEventListener("submit", event => loadExact(event, "relation-init-sessions", "init-id", "init-details"));
  byId("load-audit").addEventListener("click", async () => {
    try { const data = await api(`/api/v1/projects/${requireProject()}/audit-events?limit=100`);
      replaceCards(byId("audit-results"), data.events.map(auditEventCard), "No audit events");
    } catch (error) { showMessage(error.message); }
  });
}

function auditEventCard(item) {
  const expected = item.expectedRevisionNo == null ? "none" : item.expectedRevisionNo;
  const card = resultCard(
    `${item.action} · ${item.subjectType}`,
    item.reason,
    `${item.actor} · ${item.origin} · request ${item.requestId} · expected revision ${expected} · ${item.occurredAt}`,
  );
  const details = document.createElement("pre");
  details.className = "details";
  details.textContent = JSON.stringify(item.details ?? {}, null, 2);
  card.append(details);
  return card;
}

async function loadExact(event, resource, inputId, outputId) {
  event.preventDefault();
  try { const data = await api(`/api/v1/projects/${requireProject()}/${resource}/${byId(inputId).value.trim()}`); byId(outputId).textContent = JSON.stringify(data, null, 2); }
  catch (error) { showMessage(error.message); }
}
