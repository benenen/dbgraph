"use strict";

const state = {
  csrf: "", role: "", actor: "", relation: null, graph: null, graphRelation: null, traceResult: null,
  editors: { guard: undefined, selector: undefined, transform: undefined }, evidence: [],
  dataSourceNames: new Map(),
};

function byId(id) { return document.getElementById(id); }
function projectId() { return byId("project-id")?.value.trim() || ""; }
function showMessage(message, good = false) {
  const element = byId("global-message") || byId("login-message");
  if (!element) return;
  element.textContent = message;
  element.classList.toggle("good", good);
}

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  if (options.body) headers.set("Content-Type", "application/json");
  if (options.method && options.method !== "GET") headers.set("X-CSRF-Token", state.csrf);
  const response = await fetch(path, { ...options, headers, credentials: "same-origin" });
  let envelope;
  try { envelope = await response.json(); } catch { throw new Error(`HTTP ${response.status}`); }
  if (!response.ok || !envelope.success) {
    if (response.status === 401 && location.pathname !== "/login") location.assign("/login");
    const error = new Error(envelope.error?.message || `HTTP ${response.status}`);
    error.details = envelope.error?.details;
    error.status = response.status;
    throw error;
  }
  return envelope.data;
}

async function initializeLogin() {
  const form = byId("login-form");
  if (!form) return false;
  form.addEventListener("submit", async event => {
    event.preventDefault();
    showMessage("");
    const token = byId("login-token").value;
    try {
      const data = await api("/login", { method: "POST", body: JSON.stringify({ token }) });
      state.csrf = data.csrfToken;
      byId("login-token").value = "";
      location.assign("/");
    } catch (error) { showMessage(error.message); }
  });
  return true;
}

async function initializeSession() {
  const session = await api("/api/v1/session");
  state.csrf = session.csrfToken;
  state.role = session.role;
  state.actor = session.actor;
  byId("session-actor").textContent = session.actor;
  byId("session-role").textContent = session.role;
}

function initializeNavigation() {
  document.querySelectorAll("[data-panel-link]").forEach(link => link.addEventListener("click", () => activatePanel(link.dataset.panelLink)));
  const initial = location.hash.slice(1);
  if (initial && byId(`panel-${initial}`)) activatePanel(initial);
  window.addEventListener("hashchange", () => {
    const name = location.hash.slice(1);
    if (name && byId(`panel-${name}`)) activatePanel(name);
  });
}

function activatePanel(name) {
  document.querySelectorAll(".panel").forEach(panel => panel.classList.toggle("active", panel.id === `panel-${name}`));
  document.querySelectorAll("[data-panel-link]").forEach(link => link.classList.toggle("active", link.dataset.panelLink === name));
  // Covers every path that lands on one of these panels without a click on
  // its nav link: the initial location.hash check and the hashchange
  // listener in initializeNavigation() both call activatePanel() directly.
  // The request-token guards in the underlying load functions make any
  // redundant call here (e.g. one that also arrives via a page-load catch-up
  // call, or the project select's "change" listener) harmless.
  if (name === "schema") loadNodeDataSources();
  if (name === "data-sources") { loadDataSourceList(); loadRepositoryList(); }
}

function applyRoleCapabilities() {
  const canCreate = ["EDITOR", "ADMIN"].includes(state.role);
  const canRevise = ["EDITOR", "REVIEWER", "ADMIN"].includes(state.role);
  const canReview = ["REVIEWER", "ADMIN"].includes(state.role);
  document.querySelectorAll("[data-capability='create']").forEach(element => { element.hidden = !canCreate; });
  document.querySelectorAll("[data-capability='relation-action'], [data-capability='graph-relation-action']").forEach(element => { element.hidden = !(canRevise || canReview); });
  document.querySelectorAll("[data-capability='admin']").forEach(element => { element.hidden = state.role !== "ADMIN"; });
  for (const select of [byId("relation-action"), byId("graph-relation-action")]) {
    for (const option of select.options) {
      const allowed = option.value === "revision" ? canRevise :
        (option.value === "tombstones" ? canCreate : canReview);
      option.disabled = !allowed;
      option.hidden = !allowed;
    }
    const firstAllowed = [...select.options].find(option => !option.disabled);
    if (firstAllowed) select.value = firstAllowed.value;
  }
}

function requireProject() {
  const value = projectId();
  if (!/^[1-9][0-9]{0,18}$/.test(value)) throw new Error("Enter a valid Project ID first.");
  return value;
}

const PROJECT_STORAGE_KEY = "dbgraph.projectId";

async function loadProjects(preferredId = "") {
  const select = byId("project-id");
  if (!select) return;
  let projects = [];
  try { projects = await api("/api/v1/projects?limit=200"); }
  catch (error) { showMessage(error.message); return; }

  const stored = preferredId || localStorage.getItem(PROJECT_STORAGE_KEY) || "";
  select.replaceChildren();
  if (!projects.length) {
    select.append(new Option("No projects yet — create one in Projects", ""));
    if (stored) localStorage.removeItem(PROJECT_STORAGE_KEY);
    return;
  }
  select.append(new Option("Select a project", ""));
  for (const project of projects) select.append(new Option(`${project.name} · ${project.id}`, project.id));

  const known = projects.some(project => project.id === stored);
  select.value = known ? stored : (projects.length === 1 ? projects[0].id : "");
  if (!known && stored) localStorage.removeItem(PROJECT_STORAGE_KEY);
  if (select.value) localStorage.setItem(PROJECT_STORAGE_KEY, select.value);
}

function initializeProjectPicker() {
  const select = byId("project-id");
  if (!select) return;
  select.addEventListener("change", () => {
    if (select.value) localStorage.setItem(PROJECT_STORAGE_KEY, select.value);
    else localStorage.removeItem(PROJECT_STORAGE_KEY);
    loadNodeDataSources();
  });
}

function dataSourceLabel(id) {
  return state.dataSourceNames.get(id) || id;
}

let nodeDataSourcesRequestToken = 0;

async function loadNodeDataSources() {
  const select = byId("node-data-source");
  if (!select) return;
  const requestToken = ++nodeDataSourcesRequestToken;
  state.dataSourceNames = new Map();
  select.replaceChildren(new Option("All data sources", ""));
  let project;
  try { project = requireProject(); } catch { return; }
  try {
    const sources = await api(`/api/v1/projects/${project}/data-sources?limit=200`);
    if (requestToken !== nodeDataSourcesRequestToken) return; // a newer call already superseded this one
    state.dataSourceNames = new Map(sources.map(source => [source.id, source.name]));
    for (const source of sources) select.append(new Option(source.name, source.id));
  } catch (error) {
    // The picker stays on "All data sources", but the operator needs to know
    // why — otherwise the filter silently degrades and rows show raw ids.
    if (requestToken === nodeDataSourcesRequestToken) showMessage(error.message);
  }
}

function showNodeDetails(node) {
  byId("node-details").textContent = JSON.stringify({ ...node, dataSourceName: dataSourceLabel(node.dataSourceId) }, null, 2);
}

function resultCard(title, detail, meta = "", state = "") {
  const card = document.createElement("article");
  card.className = "result-card";
  if (state) card.dataset.state = state;
  const strong = document.createElement("strong"); strong.textContent = title;
  const body = document.createElement("small"); body.textContent = detail;
  card.append(strong, body);
  if (meta) { const extra = document.createElement("small"); extra.textContent = meta; card.append(extra); }
  return card;
}

function replaceCards(container, cards, emptyText) {
  container.replaceChildren(...(cards.length ? cards : [resultCard(emptyText, "No matching records were returned.")]));
}

function initializeNodeSearch() {
  byId("node-search-form").addEventListener("submit", async event => {
    event.preventDefault();
    try {
      const project = requireProject();
      const query = byId("node-query").value;
      const source = byId("node-data-source").value;
      const suffix = source ? `&dataSourceId=${encodeURIComponent(source)}` : "";
      const data = await api(`/api/v1/projects/${project}/nodes?q=${encodeURIComponent(query)}&limit=50${suffix}`);
      replaceCards(byId("node-results"), data.nodes.map(node => {
        const card = resultCard(node.qualifiedName, `${node.kind} · ${node.dataType || "no type"}`, `ID ${node.id} · ${dataSourceLabel(node.dataSourceId)}`);
        card.addEventListener("click", () => {
          byId("trace-start").value = node.id;
          byId("proposal-source").value = node.id;
          if (state.editors.transform?.kind === "column_copy") updateEditor("transform", { kind: "column_copy", nodeId: node.id }, true);
          showNodeDetails(node);
          location.hash = "node-details";
          activatePanel("node-details");
        });
        return card;
      }), "No nodes found");
    } catch (error) { showMessage(error.message); }
  });
}

function initializeGraphNodeSearch() {
  byId("graph-node-search-form").addEventListener("submit", async event => {
    event.preventDefault();
    try {
      const data = await api(`/api/v1/projects/${requireProject()}/nodes?q=${encodeURIComponent(byId("graph-node-query").value)}&limit=50`);
      replaceCards(byId("graph-node-results"), data.nodes.map(node => {
        const card = resultCard(node.qualifiedName, `${node.kind} · ${node.dataType || "no type"}`, `ID ${node.id}`);
        const actions = document.createElement("div");
        actions.className = "card-actions";
        for (const destination of ["start", "target"]) {
          const button = builderButton(`Use as ${destination}`, `use-as-${destination}`, `graph-node.${node.id}`, () => {
            byId(`trace-${destination}`).value = node.id;
            showMessage(`${node.qualifiedName} selected as trace ${destination}.`, true);
          });
          actions.append(button);
        }
        card.append(actions);
        return card;
      }), "No graph nodes found");
    } catch (error) { showMessage(error.message); }
  });
}

function initializeTrace() {
  byId("trace-form").addEventListener("submit", async event => {
    event.preventDefault();
    try {
      const project = requireProject();
      const payload = {
        startNodeId: byId("trace-start").value.trim(), targetNodeId: byId("trace-target").value.trim(),
        direction: byId("trace-direction").value, maxDepth: Number(byId("trace-depth").value),
      };
      const evaluationContext = parseOptionalJSON("trace-context", "Evaluation context");
      if (evaluationContext !== undefined) {
        if (!evaluationContext || Array.isArray(evaluationContext) || typeof evaluationContext !== "object") {
          throw new Error("Evaluation context must be a JSON object.");
        }
        payload.context = evaluationContext;
      }
      const data = await api(`/api/v1/projects/${project}/graph-traces`, { method: "POST", body: JSON.stringify(payload) });
      state.traceResult = data;
      renderGraph(data);
      showMessage(`Trace visited ${data.visitedNodes} nodes.`, true);
    } catch (error) { showMessage(error.message); }
  });
}

function renderGraph(data) {
  const elements = new Map();
  const visibleRelations = new Map();
  const flags = [];
  const relationType = byId("trace-type-filter").value;
  const status = byId("trace-status-filter").value;
  const minimumConfidence = Number(byId("trace-confidence-filter").value);
  if (data.cycleDetected) flags.push(flag("Cycle detected", true));
  if (data.truncated) flags.push(flag("Result truncated", true));
  for (const path of data.paths) {
    for (const step of path.steps) {
      const matchesStatus = status === "ALL" || step.status === status || step.proposalStatus === status;
      if ((relationType !== "ALL" && step.type !== relationType) ||
          !matchesStatus || step.confidence < minimumConfidence) continue;
      elements.set(`n:${step.sourceNodeId}`, { data: { id: step.sourceNodeId, label: step.sourceNodeId } });
      elements.set(`n:${step.targetNodeId}`, { data: { id: step.targetNodeId, label: step.targetNodeId } });
      const id = `e:${step.relationId}`;
      const statusLabel = step.proposalStatus ? `${step.status} + ${step.proposalStatus}` : step.status;
      elements.set(id, { data: { id, relationId: step.relationId, source: step.sourceNodeId, target: step.targetNodeId, label: `${step.type} · ${statusLabel} · ${step.truth}`, truth: step.truth, status: step.status, proposalStatus: step.proposalStatus || "", confidence: step.confidence } });
      visibleRelations.set(step.relationId, step);
      if (step.truth === "UNKNOWN") flags.push(flag(`Relation ${step.relationId} needs context`, false));
    }
  }
  byId("trace-flags").replaceChildren(...flags);
  const relationCount = visibleRelations.size;
  byId("graph-visible-summary").textContent = `${relationCount} ${relationCount === 1 ? "relation" : "relations"} shown`;
  renderGraphRelationResults([...visibleRelations.values()]);
  byId("graph-details").textContent = JSON.stringify(data, null, 2);
  if (!window.cytoscape) return;
  if (state.graph) state.graph.destroy();
  state.graph = window.cytoscape({
    container: byId("graph-canvas"), elements: [...elements.values()],
    style: [
      { selector: "node", style: { "background-color": "#0d1b21", label: "data(label)", color: "#0d1b21", "font-size": 10, "text-valign": "bottom", "text-margin-y": 8 } },
      { selector: "edge", style: { width: 2, "line-color": "#8fa1a4", "target-arrow-color": "#8fa1a4", "target-arrow-shape": "triangle", "curve-style": "bezier", label: "data(label)", "font-size": 8, "text-background-opacity": 1, "text-background-color": "#f7f9f9" } },
      { selector: "edge[truth = 'UNKNOWN']", style: { "line-style": "dashed", "line-color": "#6a4fbf", "target-arrow-color": "#6a4fbf" } },
    ], layout: { name: "breadthfirst", directed: true, padding: 30 },
  });
  state.graph.on("tap", "node", async event => {
    const nodeID = event.target.id();
    byId("trace-start").value = nodeID;
    try {
      const node = await api(`/api/v1/projects/${requireProject()}/nodes/${nodeID}`);
      showNodeDetails(node);
      location.hash = "node-details";
      activatePanel("node-details");
    } catch (error) { showMessage(error.message); }
  });
  state.graph.on("tap", "edge", event => loadGraphRelation(event.target.data("relationId") || event.target.id().replace(/^e:/, "")));
}

function renderGraphRelationResults(steps) {
  const cards = steps.map(step => {
    const card = resultCard(`${step.type} · ${step.truth}`, `Relation ${step.relationId}`, `${step.status} · confidence ${step.confidence}`, step.truth);
    card.classList.add("graph-edge-item");
    card.append(builderButton(`Inspect relation ${step.relationId}`, "inspect-relation", `graph-relation.${step.relationId}`, () => loadGraphRelation(step.relationId)));
    return card;
  });
  byId("graph-edge-results").replaceChildren(...cards);
}

async function loadGraphRelation(relationID) {
  try {
    const relation = await api(`/api/v1/projects/${requireProject()}/relations/${relationID}`);
    showRelation(relation);
    showGraphRelation(relation);
  } catch (error) { showMessage(error.message); }
}

function showGraphRelation(relation) {
  state.graphRelation = relation;
  const revision = relation.active || relation.proposed;
  byId("graph-edge-panel").hidden = false;
  byId("graph-edge-title").textContent = `Relation ${relation.id}`;
  byId("graph-edge-summary").textContent = `${relation.type} · ${relation.status} · revision ${relation.latestRevisionNo}`;
  byId("graph-edge-guard").textContent = JSON.stringify(revision?.guard ?? null, null, 2);
  byId("graph-edge-selector").textContent = JSON.stringify(revision?.selector ?? null, null, 2);
  byId("graph-edge-transform").textContent = JSON.stringify(revision?.transform ?? null, null, 2);
  byId("graph-edge-evidence").textContent = JSON.stringify(revision?.evidence ?? [], null, 2);
  byId("graph-action-revision").value = relation.latestRevisionNo || "";
}

function initializeGraphRelationActions() {
  byId("graph-relation-action-form").addEventListener("submit", async event => {
    event.preventDefault();
    try {
      const relation = state.graphRelation;
      if (!relation) throw new Error("Inspect a graph relation first.");
      const action = byId("graph-relation-action").value;
      if (action === "revision") {
        byId("relation-action").value = "revision";
        location.hash = "relation";
        activatePanel("relation");
        showMessage(`Relation ${relation.id} is ready for a structured revision.`, true);
        return;
      }
      const reason = byId("graph-action-reason").value.trim();
      if (!reason) throw new Error("Enter a reason for the graph relation action.");
      const payload = { expectedRevisionNo: Number(byId("graph-action-revision").value), reason };
      if (action === "reviews") payload.decision = byId("graph-action-decision").value;
      const updated = await api(`/api/v1/projects/${requireProject()}/relations/${relation.id}/${action}`, { method: "POST", body: JSON.stringify(payload) });
      showRelation(updated);
      showGraphRelation(updated);
      showMessage("Graph relation action completed.", true);
    } catch (error) {
      showMessage(error.details?.currentRevisionNo ? `${error.message} Current revision: ${error.details.currentRevisionNo}. Reload before retrying.` : error.message);
    }
  });
}

function flag(text, warning) { const element = document.createElement("span"); element.textContent = text; if (warning) element.className = "warn"; return element; }

function parseOptionalJSON(elementId, label) {
  const textValue = byId(elementId).value.trim();
  if (!textValue) return undefined;
  try { return JSON.parse(textValue); }
  catch { throw new Error(`${label} must be valid JSON.`); }
}

const conditionKinds = ["compare", "and", "or", "not", "in", "not_in", "is_null", "is_not_null"];
const comparisonOperators = ["eq", "ne", "gt", "gte", "lt", "lte"];
const literalTypes = ["string", "integer", "decimal", "boolean", "null"];

function cloneData(value) {
  return value === undefined ? undefined : JSON.parse(JSON.stringify(value));
}

function defaultLiteral(type = "string") {
  const value = type === "boolean" ? false : (type === "null" ? null : "");
  return { kind: "literal", valueType: type, value };
}

function defaultValue(kind = "column", transform = false) {
  if (kind === "literal") return defaultLiteral();
  if (kind === "parameter") return { kind, parameter: "" };
  if (kind === "case") {
    return {
      kind, cases: [{ when: defaultCondition(), then: defaultValue("column_copy", true) }],
      else: defaultValue("column_copy", true),
    };
  }
  return { kind: transform ? "column_copy" : "column", nodeId: "" };
}

function defaultCondition(kind = "compare") {
  if (kind === "and" || kind === "or") return { kind, children: [defaultCondition(), defaultCondition()] };
  if (kind === "not") return { kind, operand: defaultCondition() };
  if (kind === "in" || kind === "not_in") return { kind, left: defaultValue(), values: [defaultLiteral()] };
  if (kind === "is_null" || kind === "is_not_null") return { kind, left: defaultValue() };
  return { kind: "compare", operator: "eq", left: defaultValue(), right: defaultLiteral() };
}

function builderElement(tag, className = "", textValue = "") {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (textValue) element.textContent = textValue;
  return element;
}

function builderSelect(options, selected, path, onChange) {
  const select = builderElement("select");
  select.dataset.builderPath = path;
  for (const value of options) {
    const option = document.createElement("option");
    option.value = value;
    option.textContent = value === "none" ? "No condition" : value.toUpperCase().replaceAll("_", " ");
    select.append(option);
  }
  select.value = selected;
  select.addEventListener("change", event => onChange(event.target.value));
  return select;
}

function builderInput(value, path, placeholder, type = "text") {
  const input = builderElement("input");
  input.type = type;
  input.value = value ?? "";
  input.placeholder = placeholder;
  input.dataset.builderPath = path;
  if (type === "number") input.min = "1";
  input.addEventListener("input", event => updateBuilderField(path, event.target.value));
  return input;
}

function labeledBuilderControl(labelText, control) {
  const label = builderElement("label", "builder-control");
  const caption = builderElement("span", "muted", labelText);
  label.append(caption, control);
  return label;
}

function builderButton(textValue, action, path, onClick) {
  const button = builderElement("button", "quiet", textValue);
  button.type = "button";
  button.dataset.builderAction = action;
  button.dataset.builderPath = path;
  button.addEventListener("click", onClick);
  return button;
}

function renderConditionNode(parent, node, path, optional = false) {
  const wrapper = builderElement("div", "condition-node");
  const kindOptions = optional ? ["none", ...conditionKinds] : conditionKinds;
  const selectedKind = node?.kind || "none";
  wrapper.append(labeledBuilderControl("Condition", builderSelect(kindOptions, selectedKind, `${path}.kind`, kind => {
    updateBuilderNode(path, kind === "none" ? undefined : defaultCondition(kind));
  })));
  if (!node) {
    parent.append(wrapper);
    return;
  }

  if (node.kind === "and" || node.kind === "or") {
    const children = node.children || [];
    children.forEach((child, index) => {
      const childContainer = builderElement("div", "builder-child");
      renderConditionNode(childContainer, child, `${path}.children.${index}`);
      if (children.length > 2) {
        childContainer.append(builderButton(`Remove condition ${index + 1}`, "remove-condition", `${path}.children.${index}`, () => {
          const current = builderModelAt(path);
          updateBuilderNode(path, { ...current, children: current.children.filter((_, itemIndex) => itemIndex !== index) });
        }));
      }
      wrapper.append(childContainer);
    });
    wrapper.append(builderButton("Add condition", "add-condition", path, () => {
      const current = builderModelAt(path);
      updateBuilderNode(path, { ...current, children: [...current.children, defaultCondition()] });
    }));
  } else if (node.kind === "not") {
    renderConditionNode(wrapper, node.operand || defaultCondition(), `${path}.operand`);
  } else if (node.kind === "compare") {
    wrapper.append(labeledBuilderControl("Operator", builderSelect(comparisonOperators, node.operator || "eq", `${path}.operator`, operator => {
      updateBuilderField(`${path}.operator`, operator);
    })));
    renderValueNode(wrapper, node.left || defaultValue(), `${path}.left`);
    renderValueNode(wrapper, node.right || defaultLiteral(), `${path}.right`);
  } else if (node.kind === "in" || node.kind === "not_in") {
    renderValueNode(wrapper, node.left || defaultValue(), `${path}.left`);
    const values = node.values || [defaultLiteral()];
    values.forEach((value, index) => {
      const valueContainer = builderElement("div", "builder-child");
      renderValueNode(valueContainer, value, `${path}.values.${index}`);
      if (values.length > 1) {
        valueContainer.append(builderButton(`Remove value ${index + 1}`, "remove-value", `${path}.values.${index}`, () => {
          const current = builderModelAt(path);
          updateBuilderNode(path, { ...current, values: current.values.filter((_, itemIndex) => itemIndex !== index) });
        }));
      }
      wrapper.append(valueContainer);
    });
    wrapper.append(builderButton("Add value", "add-value", path, () => {
      const current = builderModelAt(path);
      updateBuilderNode(path, { ...current, values: [...current.values, defaultLiteral()] });
    }));
  } else {
    renderValueNode(wrapper, node.left || defaultValue(), `${path}.left`);
  }
  parent.append(wrapper);
}

function renderValueNode(parent, value, path, transform = false) {
  const wrapper = builderElement("div", "value-node");
  const kinds = transform ? ["column_copy", "literal", "parameter", "case"] : ["column", "literal", "parameter"];
  const selectedKind = kinds.includes(value?.kind) ? value.kind : kinds[0];
  wrapper.append(labeledBuilderControl("Value", builderSelect(kinds, selectedKind, `${path}.kind`, kind => {
    updateBuilderNode(path, defaultValue(kind, transform));
  })));
  if (selectedKind === "column" || selectedKind === "column_copy") {
    wrapper.append(labeledBuilderControl("Column node ID", builderInput(value?.nodeId, `${path}.nodeId`, "Column node ID", "number")));
  } else if (selectedKind === "parameter") {
    wrapper.append(labeledBuilderControl("Parameter", builderInput(value?.parameter, `${path}.parameter`, "Parameter name")));
  } else if (selectedKind === "literal") {
    const literal = canonicalValue(value || defaultLiteral());
    wrapper.append(labeledBuilderControl("Literal type", builderSelect(literalTypes, literal.valueType || "string", `${path}.valueType`, type => {
      updateBuilderNode(path, defaultLiteral(type));
    })));
    if (literal.valueType !== "null") {
      if (literal.valueType === "boolean") {
        wrapper.append(labeledBuilderControl("Literal value", builderSelect(["false", "true"], String(Boolean(literal.value)), `${path}.value`, raw => {
          updateBuilderField(`${path}.value`, raw === "true");
        })));
      } else {
        wrapper.append(labeledBuilderControl("Literal value", builderInput(literal.value, `${path}.value`, "Literal value")));
      }
    }
  } else if (selectedKind === "case") {
    const cases = value?.cases?.length ? value.cases : defaultValue("case", true).cases;
    cases.forEach((branch, index) => {
      const branchContainer = builderElement("section", "case-branch");
      branchContainer.append(builderElement("strong", "", `WHEN ${index + 1}`));
      renderConditionNode(branchContainer, branch.when, `${path}.cases.${index}.when`);
      renderValueNode(branchContainer, branch.then, `${path}.cases.${index}.then`, true);
      if (cases.length > 1) {
        branchContainer.append(builderButton(`Remove case ${index + 1}`, "remove-case", `${path}.cases.${index}`, () => {
          const current = builderModelAt(path);
          updateBuilderNode(path, { ...current, cases: current.cases.filter((_, itemIndex) => itemIndex !== index) });
        }));
      }
      wrapper.append(branchContainer);
    });
    wrapper.append(builderButton("Add case", "add-case", path, () => {
      const current = builderModelAt(path);
      updateBuilderNode(path, { ...current, cases: [...current.cases, { when: defaultCondition(), then: defaultValue("column_copy", true) }] });
    }));
    const elseContainer = builderElement("section", "case-branch");
    elseContainer.append(builderElement("strong", "", "ELSE"));
    renderValueNode(elseContainer, value?.else || defaultValue("column_copy", true), `${path}.else`, true);
    wrapper.append(elseContainer);
  }
  parent.append(wrapper);
}

function updateEditor(name, value, rerender = false) {
  state.editors = { ...state.editors, [name]: cloneData(value) };
  if (rerender) renderStructuredEditors();
  updateEditorPreviews();
  refreshDraftDiff();
}

function immutableSet(value, path, replacement) {
  if (path.length === 0) return replacement;
  const [head, ...tail] = path;
  if (Array.isArray(value)) {
    const index = Number(head);
    return value.map((item, itemIndex) => itemIndex === index ? immutableSet(item, tail, replacement) : item);
  }
  return { ...(value || {}), [head]: immutableSet(value?.[head], tail, replacement) };
}

function updateBuilderField(path, value) {
  const [name, ...parts] = path.split(".");
  updateEditor(name, immutableSet(state.editors[name], parts, value));
}

function builderModelAt(path) {
  const [name, ...parts] = path.split(".");
  return parts.reduce((value, part) => value?.[Array.isArray(value) ? Number(part) : part], state.editors[name]);
}

function updateBuilderNode(path, value) {
  updateBuilderField(path, value);
  renderStructuredEditors();
  updateEditorPreviews();
}

function renderStructuredEditors() {
  const definitions = [
    ["guard", "guard-builder", true], ["selector", "selector-builder", true], ["transform", "transform-builder", false],
  ];
  for (const [name, containerID, optional] of definitions) {
    const container = byId(containerID);
    container.replaceChildren();
    if (name === "transform") {
      renderValueNode(container, state.editors.transform || defaultValue("column_copy", true), name, true);
    } else {
      renderConditionNode(container, state.editors[name], name, optional);
    }
  }
}

function updateEditorPreviews() {
  byId("guard-preview").textContent = JSON.stringify(state.editors.guard ?? null, null, 2);
  byId("selector-preview").textContent = JSON.stringify(state.editors.selector ?? null, null, 2);
  byId("transform-preview").textContent = JSON.stringify(state.editors.transform ?? null, null, 2);
}

function guardFromForm() { return cloneData(state.editors.guard); }
function selectorFromForm() { return cloneData(state.editors.selector); }
function transformFromForm() {
  if (!state.editors.transform) throw new Error("Build a transform.");
  return cloneData(state.editors.transform);
}

function editorPayload() {
  return {
    sourceNodeId: byId("proposal-source").value.trim(), targetNodeId: byId("proposal-target").value.trim(),
    guard: guardFromForm(), selector: selectorFromForm(),
	transform: transformFromForm(),
    confidence: Number(byId("proposal-confidence").value), evidence: evidenceFromForm(),
  };
}

function initializeProposal() {
  byId("proposal-form").addEventListener("submit", async event => {
    event.preventDefault();
    try {
      if (!["EDITOR", "ADMIN"].includes(state.role)) throw new Error("Your role cannot create relations.");
      const project = requireProject();
      const payload = { type: byId("proposal-type").value, ...editorPayload(), reason: byId("proposal-reason").value };
      const relation = await api(`/api/v1/projects/${project}/relations`, { method: "POST", body: JSON.stringify(payload) });
      state.relation = relation; byId("relation-id").value = relation.id; showRelation(relation);
      showMessage(`Proposal ${relation.id} created. It is not effective until review.`, true);
    } catch (error) { showMessage(error.message); }
  });
}

function showRelation(relation) {
  byId("relation-details").textContent = JSON.stringify(relation, null, 2);
  byId("action-revision").value = relation.latestRevisionNo || "";
  state.relation = relation;
  const editable = relation.proposed || relation.active;
  if (editable) populateEditor(editable);
  renderRevisionDiff(relation.active, editorDraft(), relation.proposed);
}

function populateEditor(revision) {
  byId("proposal-source").value = revision.sourceNodeId || "";
  byId("proposal-target").value = revision.targetNodeId || "";
  byId("proposal-confidence").value = revision.confidence ?? 0.9;
  state.editors = {
    guard: canonicalCondition(revision.guard), selector: canonicalCondition(revision.selector),
    transform: canonicalValue(revision.transform) || defaultValue("column_copy", true),
  };
  state.evidence = cloneData(revision.evidence || []);
  renderStructuredEditors();
  updateEditorPreviews();
  renderEvidenceList();
}

function canonicalCondition(condition) {
  if (!condition) return undefined;
  const result = cloneData(condition);
  if (result.children) result.children = result.children.map(canonicalCondition);
  if (result.operand) result.operand = canonicalCondition(result.operand);
  if (result.left) result.left = canonicalValue(result.left);
  if (result.right) result.right = canonicalValue(result.right);
  if (result.values) result.values = result.values.map(canonicalValue);
  return result;
}

function canonicalValue(value) {
  if (!value) return undefined;
  const result = cloneData(value);
  if (result.literal) {
    result.valueType = result.literal.type;
    result.value = result.literal.value;
    delete result.literal;
  }
  if (result.cases) result.cases = result.cases.map(branch => ({
    when: canonicalCondition(branch.when), then: canonicalValue(branch.then),
  }));
  if (result.else) result.else = canonicalValue(result.else);
  return result;
}

function editorDraft() {
  return {
    sourceNodeId: byId("proposal-source").value.trim(), targetNodeId: byId("proposal-target").value.trim(),
    guard: cloneData(state.editors.guard), selector: cloneData(state.editors.selector),
    transform: cloneData(state.editors.transform), confidence: Number(byId("proposal-confidence").value),
    evidence: cloneData(state.evidence),
  };
}

function refreshDraftDiff() {
  if (!state.relation || !byId("revision-diff")) return;
  renderRevisionDiff(state.relation.active, editorDraft(), state.relation.proposed);
}

function renderRevisionDiff(active, draft, proposed) {
  const container = byId("revision-diff");
  const cells = [diffCell("Field", true), diffCell("Active", true), diffCell("Draft", true), diffCell("Proposed", true)];
  for (const field of ["sourceNodeId", "targetNodeId", "guard", "selector", "transform", "confidence", "evidence"]) {
    const activeValue = formatDiffValue(active?.[field]);
    const draftValue = formatDiffValue(draft?.[field]);
    const proposedValue = formatDiffValue(proposed?.[field]);
    cells.push(
      diffCell(field, false, field, "field"), diffCell(activeValue, false, field, "active"),
      diffCell(draftValue, false, field, "draft", activeValue !== draftValue),
      diffCell(proposedValue, false, field, "proposed"),
    );
  }
  container.replaceChildren(...cells);
}

function diffCell(value, heading = false, field = "", column = "", changed = false) {
  const cell = document.createElement("div");
  if (heading) cell.className = "diff-head";
  if (field) cell.dataset.diffField = field;
  if (column) cell.dataset.diffColumn = column;
  if (changed) cell.classList.add("diff-changed");
  cell.textContent = value;
  return cell;
}

function formatDiffValue(value) {
  return value === undefined || value === null ? "—" : (typeof value === "string" ? value : JSON.stringify(value, null, 2));
}

function initializeRelationActions() {
  byId("relation-load-form").addEventListener("submit", async event => {
    event.preventDefault();
    try { const relation = await api(`/api/v1/projects/${requireProject()}/relations/${byId("relation-id").value.trim()}`); showRelation(relation); }
    catch (error) { showMessage(error.message); }
  });
  byId("relation-action-form").addEventListener("submit", async event => {
    event.preventDefault();
    try {
      const project = requireProject(); const relationId = byId("relation-id").value.trim(); const action = byId("relation-action").value;
      const payload = { expectedRevisionNo: Number(byId("action-revision").value), reason: byId("action-reason").value };
      if (action === "reviews") payload.decision = byId("action-decision").value;
      if (action === "revision") {
        if (!state.relation?.active && !state.relation?.proposed) throw new Error("Load a relation with content before proposing a revision.");
        Object.assign(payload, editorPayload());
      }
      const suffix = action === "revision" ? "revisions" : action;
      const relation = await api(`/api/v1/projects/${project}/relations/${relationId}/${suffix}`, { method: "POST", body: JSON.stringify(payload) });
      showRelation(relation); showMessage("Relation command completed.", true);
    } catch (error) {
      showMessage(error.details?.currentRevisionNo ? `${error.message} Current revision: ${error.details.currentRevisionNo}. Reload before retrying.` : error.message);
    }
  });
}

function initializeReview() {
  byId("load-proposals").addEventListener("click", async () => {
    try {
      const data = await api(`/api/v1/projects/${requireProject()}/relation-proposals?limit=100`);
      replaceCards(byId("proposal-results"), data.relations.map(relation => {
        const card = resultCard(`${relation.type} · ${relation.status}`, `Revision ${relation.latestRevisionNo}`, `Relation ${relation.id}`, relation.status);
        card.addEventListener("click", () => { byId("relation-id").value = relation.id; showRelation(relation); location.hash = "relation"; activatePanel("relation"); });
        return card;
      }), "No pending proposals");
      if (data.truncated) showMessage("Proposal results were truncated. Narrow the project or reload a smaller page.");
    } catch (error) { showMessage(error.message); }
  });
}

async function initializeApp() {
  if (await initializeLogin()) return;
  try {
    await initializeSession(); initializeNavigation(); applyRoleCapabilities(); initializeProjectPicker(); await loadProjects();
    // initializeNavigation() runs (and may activate Schema Explorer or Data
    // sources from location.hash) before loadProjects() has restored/auto-
    // selected a project, so that first activatePanel() call finds no
    // project yet and its project-scoped load calls are no-ops. Catch up now
    // that a project is known, guarded by the same request tokens.
    if (byId("panel-schema").classList.contains("active")) loadNodeDataSources();
    if (byId("panel-data-sources").classList.contains("active")) { loadDataSourceList(); loadRepositoryList(); }
    initializeNodeSearch(); initializeGraphNodeSearch(); initializeTrace(); initializeStructuredEditors(); initializeProposal();
    initializeRelationActions(); initializeGraphRelationActions(); initializeReview(); initializeQueries(); initializeAdmin(); initializeWorkspace();
    ["trace-type-filter", "trace-status-filter", "trace-confidence-filter"].forEach(id => byId(id).addEventListener("change", () => {
      if (state.traceResult) renderGraph(state.traceResult);
    }));
    byId("logout-button").addEventListener("click", async () => { try { await api("/logout", { method: "POST" }); location.assign("/login"); } catch (error) { showMessage(error.message); } });
  } catch (error) { showMessage(error.message); }
}

document.addEventListener("DOMContentLoaded", initializeApp);
