"use strict";

function evidenceFromForm() {
  if (state.evidence.length === 0) throw new Error("Add at least one evidence entry.");
  return cloneData(state.evidence);
}

function evidenceDraft() {
  const repository = byId("evidence-repository").value.trim();
  const commit = byId("evidence-commit").value.trim();
  const file = byId("evidence-file").value.trim();
  const startLine = Number(byId("evidence-start").value);
  const endLine = Number(byId("evidence-end").value);
  if (!repository || !commit || !file || !Number.isInteger(startLine) || !Number.isInteger(endLine) || startLine < 1 || endLine < startLine) {
    throw new Error("Evidence requires repository, commit, file, and a valid line range.");
  }
  return {
    kind: byId("evidence-kind").value, repository, commit, file,
    symbol: byId("evidence-symbol").value.trim(), startLine, endLine,
  };
}

function renderEvidenceList() {
  const cards = state.evidence.map((item, index) => {
    const card = resultCard(`${item.kind} · ${item.file}`, `${item.repository}@${item.commit}`, `${item.symbol || "No symbol"} · lines ${item.startLine}-${item.endLine}`);
    card.classList.add("evidence-item");
    const remove = builderButton(`Remove evidence ${index + 1}`, "remove-evidence", `evidence.${index}`, () => {
      state.evidence = state.evidence.filter((_, itemIndex) => itemIndex !== index);
      renderEvidenceList();
    });
    remove.setAttribute("aria-label", `Remove evidence ${index + 1}`);
    card.append(remove);
    return card;
  });
  byId("evidence-list").replaceChildren(...cards);
  refreshDraftDiff();
}

function initializeStructuredEditors() {
  state.editors = { guard: undefined, selector: undefined, transform: defaultValue("column_copy", true) };
  renderStructuredEditors();
  updateEditorPreviews();
  renderEvidenceList();
  byId("add-evidence").addEventListener("click", () => {
    try {
      state.evidence = [...state.evidence, evidenceDraft()];
      renderEvidenceList();
      for (const id of ["evidence-file", "evidence-symbol"]) byId(id).value = "";
    } catch (error) { showMessage(error.message); }
  });
  for (const id of ["proposal-source", "proposal-target", "proposal-confidence"]) {
    byId(id).addEventListener("input", refreshDraftDiff);
  }
}
