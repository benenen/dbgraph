"""Real-browser dbgraph login, proposal, review, revision, and trace flow."""

import json
import os
from pathlib import Path

from playwright.sync_api import expect, sync_playwright


BASE_URL = os.environ["DBGRAPH_BROWSER_BASE_URL"]
TOKEN = os.environ["DBGRAPH_BROWSER_TOKEN"]
REVIEWER_TOKEN = os.environ["DBGRAPH_BROWSER_REVIEWER_TOKEN"]
PROJECT_ID = os.environ["DBGRAPH_BROWSER_PROJECT_ID"]
SOURCE_NODE_ID = os.environ["DBGRAPH_BROWSER_SOURCE_NODE_ID"]
TARGET_NODE_ID = os.environ["DBGRAPH_BROWSER_TARGET_NODE_ID"]
ARTIFACTS = Path(os.environ["DBGRAPH_BROWSER_ARTIFACTS"])


def run_flow(page):
    page.goto(f"{BASE_URL}/login")
    page.locator("#login-token").fill(TOKEN)
    page.get_by_role("button", name="Sign in").click()
    page.wait_for_url(f"{BASE_URL}/")
    expect(page.locator("#session-role")).to_have_text("ADMIN")
    page.locator("#project-id").select_option(PROJECT_ID)
    assert_literal_guard_roundtrip(page)

    page.get_by_role("link", name="Schema Explorer").click()
    page.locator("#node-query").fill("source_value")
    page.locator("#node-search-form button").click()
    source_card = page.locator("#node-results .result-card").filter(has_text="e2e.mapping.source_value")
    expect(source_card).to_have_count(1)
    source_card.click()
    expect(page.locator("#node-details")).to_contain_text("e2e.mapping.source_value")

    page.get_by_role("link", name="Relation Details").click()
    page.locator("#proposal-source").fill(SOURCE_NODE_ID)
    page.locator("#proposal-target").fill(TARGET_NODE_ID)
    page.locator("#proposal-confidence").fill("0.90")
    build_complex_relation(page)
    add_evidence(page, "mapping/service.go", "CopyValue")
    add_evidence(page, "mapping/mapper.xml", "updateTarget")
    expect(page.locator("#evidence-list .evidence-item")).to_have_count(2)
    page.get_by_role("button", name="Remove evidence 1").click()
    expect(page.locator("#evidence-list .evidence-item")).to_have_count(1)
    add_evidence(page, "mapping/service.go", "CopyValue")
    expect(page.locator("#evidence-list .evidence-item")).to_have_count(2)
    page.locator("#proposal-reason").fill("Browser E2E code analysis evidence")
    page.get_by_role("button", name="Submit proposal").click()
    expect(page.locator("#global-message")).to_contain_text("created")
    relation_id = page.locator("#relation-id").input_value()
    assert relation_id.isdigit(), f"invalid relation ID: {relation_id!r}"
    expect(page.locator("#relation-details")).to_contain_text('"kind": "and"')
    expect(page.locator("#relation-details")).to_contain_text('"kind": "or"')
    expect(page.locator("#relation-details")).to_contain_text('"kind": "not_in"')
    expect(page.locator("#relation-details")).to_contain_text('"kind": "case"')
    expect(page.locator("#evidence-list .evidence-item")).to_have_count(2)

    approve_pending_relation(page, relation_id, "Approve browser E2E proposal")
    expect(page.locator("#relation-details")).to_contain_text('"status": "APPROVED"')
    expect(page.locator("#guard-preview")).to_contain_text('"parameter": "tenant"')
    expect(page.locator("#selector-preview")).to_contain_text('"kind": "not_in"')
    expect(page.locator("#transform-preview")).to_contain_text('"kind": "case"')
    expect(page.locator("#evidence-list .evidence-item")).to_have_count(2)

    page.locator("#proposal-confidence").fill("0.77")
    expect(page.locator('[data-diff-field="confidence"][data-diff-column="active"]')).to_have_text("0.9")
    expect(page.locator('[data-diff-field="confidence"][data-diff-column="draft"]')).to_have_text("0.77")
    page.locator("#relation-action").select_option("revision")
    page.locator("#action-reason").fill("Re-submit loaded advanced AST without data loss")
    page.locator("#relation-action-form button").click()
    expect(page.locator("#relation-details")).to_contain_text('"proposed": {')
    expect(page.locator("#relation-details")).to_contain_text('"parameter": "tenant"')

    approve_pending_relation(page, relation_id, "Approve AST-preserving revision")
    expect(page.locator("#relation-details")).to_contain_text('"status": "APPROVED"')

    page.get_by_role("link", name="Graph Explorer").click()
    select_graph_node(page, "source_value", "start")
    select_graph_node(page, "target_value", "target")
    expect(page.locator("#trace-start")).to_have_value(SOURCE_NODE_ID)
    expect(page.locator("#trace-target")).to_have_value(TARGET_NODE_ID)

    page.locator("#trace-context").fill("")
    page.get_by_role("button", name="Run trace").click()
    expect(page.locator("#graph-visible-summary")).to_have_text("1 relation shown")
    expect(page.locator("#trace-flags")).to_contain_text("needs context")
    expect(page.locator("#graph-details")).to_contain_text('"truth": "UNKNOWN"')

    page.locator("#trace-type-filter").select_option("DECLARED_FOREIGN_KEY")
    expect(page.locator("#graph-visible-summary")).to_have_text("0 relations shown")
    page.locator("#trace-type-filter").select_option("CONDITIONAL_VALUE_COPY")
    page.locator("#trace-status-filter").select_option("PROPOSED")
    expect(page.locator("#graph-visible-summary")).to_have_text("0 relations shown")
    page.locator("#trace-status-filter").select_option("APPROVED")
    expect(page.locator("#graph-visible-summary")).to_have_text("1 relation shown")
    page.locator("#trace-confidence-filter").fill("0.80")
    page.locator("#trace-confidence-filter").press("Tab")
    expect(page.locator("#graph-visible-summary")).to_have_text("0 relations shown")
    page.locator("#trace-confidence-filter").fill("0.70")
    page.locator("#trace-confidence-filter").press("Tab")
    expect(page.locator("#graph-visible-summary")).to_have_text("1 relation shown")

    page.get_by_role("button", name=f"Inspect relation {relation_id}").click()
    expect(page.locator("#graph-edge-guard")).to_contain_text('"kind": "and"')
    expect(page.locator("#graph-edge-selector")).to_contain_text('"kind": "not_in"')
    expect(page.locator("#graph-edge-transform")).to_contain_text('"kind": "case"')
    expect(page.locator("#graph-edge-evidence")).to_contain_text("mapping/service.go")
    expect(page.locator("#graph-edge-evidence")).to_contain_text("mapping/mapper.xml")
    page.locator("#graph-relation-action").select_option("revision")
    page.get_by_role("button", name="Apply graph action").click()
    expect(page.locator("#panel-relation")).to_have_class("panel active")
    expect(page.locator("#relation-action")).to_have_value("revision")

    page.get_by_role("link", name="Graph Explorer").click()
    evaluation_context = {
        "columns": {SOURCE_NODE_ID: "north", TARGET_NODE_ID: "ready"},
        "parameters": {"tenant": "north", "fallback": "ready"},
    }
    page.locator("#trace-context").fill(json.dumps(evaluation_context))
    page.locator("#trace-form button").click()
    expect(page.locator("#global-message")).to_contain_text("Trace visited")
    expect(page.locator("#graph-details")).to_contain_text(relation_id)
    expect(page.locator("#graph-details")).to_contain_text('"truth": "TRUE"')
    page.locator("#trace-direction").select_option("UPSTREAM")
    page.locator("#trace-start").fill(TARGET_NODE_ID)
    page.locator("#trace-target").fill(SOURCE_NODE_ID)
    page.get_by_role("button", name="Run trace").click()
    expect(page.locator("#graph-details")).to_contain_text(relation_id)
    expect(page.locator("#graph-details")).to_contain_text('"truth": "TRUE"')
    page.evaluate("nodeID => state.graph.getElementById(nodeID).emit('tap')", TARGET_NODE_ID)
    expect(page.locator("#panel-node-details")).to_have_class("panel active")
    expect(page.locator("#node-details")).to_contain_text("e2e.mapping.target_value")
    return relation_id


def run_reviewer_flow(browser, relation_id):
    context = browser.new_context(ignore_https_errors=True)
    page = context.new_page()
    try:
        page.goto(f"{BASE_URL}/login")
        page.locator("#login-token").fill(REVIEWER_TOKEN)
        page.get_by_role("button", name="Sign in").click()
        page.wait_for_url(f"{BASE_URL}/")
        expect(page.locator("#session-role")).to_have_text("REVIEWER")
        page.locator("#project-id").select_option(PROJECT_ID)
        page.get_by_role("link", name="Relation Details").click()
        expect(page.locator("#proposal-form")).to_be_visible()
        expect(page.locator("#submit-proposal")).to_have_count(1)
        expect(page.locator("#submit-proposal")).to_be_hidden()
        expect(page.locator('#relation-action option[value="revision"]')).to_be_enabled()
        expect(page.locator('#relation-action option[value="tombstones"]')).to_be_hidden()
        expect(page.locator('#relation-action option[value="reviews"]')).to_be_enabled()
        expect(page.locator('#relation-action option[value="suppress"]')).to_be_enabled()
        expect(page.locator('#relation-action option[value="restore"]')).to_be_enabled()

        page.get_by_role("link", name="Graph Explorer").click()
        page.locator("#trace-start").fill(SOURCE_NODE_ID)
        page.locator("#trace-target").fill(TARGET_NODE_ID)
        page.locator("#trace-context").fill(json.dumps({
            "columns": {SOURCE_NODE_ID: "north", TARGET_NODE_ID: "ready"},
            "parameters": {"tenant": "north", "fallback": "ready"},
        }))
        page.get_by_role("button", name="Run trace").click()
        page.get_by_role("button", name=f"Inspect relation {relation_id}").click()
        expect(page.locator('#graph-relation-action option[value="revision"]')).to_be_enabled()
        expect(page.locator('#graph-relation-action option[value="tombstones"]')).to_be_hidden()
        expect(page.locator('#graph-relation-action option[value="reviews"]')).to_be_enabled()
        expect(page.locator('#graph-relation-action option[value="suppress"]')).to_be_enabled()
        expect(page.locator('#graph-relation-action option[value="restore"]')).to_be_enabled()

        page.locator("#graph-relation-action").select_option("revision")
        page.get_by_role("button", name="Apply graph action").click()
        expect(page.locator("#panel-relation")).to_have_class("panel active")
        expect(page.locator("#guard-preview")).to_contain_text('"kind": "and"')
        expect(page.locator("#submit-proposal")).to_be_hidden()
        page.get_by_role("link", name="Graph Explorer").click()
        page.locator("#graph-relation-action").select_option("suppress")
        page.locator("#graph-action-reason").fill("Reviewer suppresses from Graph Explorer")
        page.get_by_role("button", name="Apply graph action").click()
        expect(page.locator("#graph-edge-summary")).to_contain_text("SUPPRESSED")
        page.locator("#graph-relation-action").select_option("restore")
        page.locator("#graph-action-reason").fill("Reviewer restores from Graph Explorer")
        page.get_by_role("button", name="Apply graph action").click()
        expect(page.locator("#graph-edge-summary")).to_contain_text("APPROVED")
    finally:
        context.close()


def select_graph_node(page, query, destination):
    page.locator("#graph-node-query").fill(query)
    page.get_by_role("button", name="Search graph nodes").click()
    card = page.locator("#graph-node-results .result-card").filter(has_text=query)
    expect(card).to_have_count(1)
    card.get_by_role("button", name=f"Use as {destination}").click()


def approve_pending_relation(page, relation_id, reason):
    page.get_by_role("link", name="Relation Review").click()
    page.locator("#load-proposals").click()
    card = page.locator("#proposal-results .result-card").filter(has_text=relation_id)
    expect(card).to_have_count(1)
    card.click()
    expect(page.locator("#panel-relation")).to_have_class("panel active")
    page.locator("#relation-action").select_option("reviews")
    page.locator("#action-decision").select_option("APPROVE")
    page.locator("#action-reason").fill(reason)
    page.locator("#relation-action-form button").click()
    expect(page.locator("#global-message")).to_contain_text("completed")


def builder(page, path):
    return page.locator(f'[data-builder-path="{path}"]')


def select_builder(page, path, value):
    builder(page, f"{path}.kind").select_option(value)


def fill_builder(page, path, value):
    builder(page, path).fill(str(value))


def build_complex_relation(page):
    select_builder(page, "guard", "and")
    fill_builder(page, "guard.children.0.left.nodeId", SOURCE_NODE_ID)
    select_builder(page, "guard.children.0.right", "parameter")
    fill_builder(page, "guard.children.0.right.parameter", "tenant")
    select_builder(page, "guard.children.1", "or")
    select_builder(page, "guard.children.1.children.0", "in")
    fill_builder(page, "guard.children.1.children.0.left.nodeId", SOURCE_NODE_ID)
    fill_builder(page, "guard.children.1.children.0.values.0.value", "north")
    select_builder(page, "guard.children.1.children.1", "not")
    select_builder(page, "guard.children.1.children.1.operand", "is_null")
    fill_builder(page, "guard.children.1.children.1.operand.left.nodeId", SOURCE_NODE_ID)

    select_builder(page, "selector", "and")
    select_builder(page, "selector.children.0", "not_in")
    fill_builder(page, "selector.children.0.left.nodeId", TARGET_NODE_ID)
    fill_builder(page, "selector.children.0.values.0.value", "blocked")
    select_builder(page, "selector.children.1", "is_not_null")
    fill_builder(page, "selector.children.1.left.nodeId", TARGET_NODE_ID)

    select_builder(page, "transform", "case")
    select_builder(page, "transform.cases.0.when.left", "parameter")
    fill_builder(page, "transform.cases.0.when.left.parameter", "tenant")
    fill_builder(page, "transform.cases.0.when.right.value", "north")
    select_builder(page, "transform.cases.0.then", "case")
    fill_builder(page, "transform.cases.0.then.cases.0.when.left.nodeId", SOURCE_NODE_ID)
    fill_builder(page, "transform.cases.0.then.cases.0.when.right.value", "north")
    fill_builder(page, "transform.cases.0.then.cases.0.then.nodeId", SOURCE_NODE_ID)
    select_builder(page, "transform.cases.0.then.else", "literal")
    fill_builder(page, "transform.cases.0.then.else.value", "fallback")
    page.locator('[data-builder-action="add-case"][data-builder-path="transform"]').click()
    select_builder(page, "transform.cases.1.when", "is_null")
    select_builder(page, "transform.cases.1.when.left", "parameter")
    fill_builder(page, "transform.cases.1.when.left.parameter", "fallback")
    select_builder(page, "transform.cases.1.then", "literal")
    fill_builder(page, "transform.cases.1.then.value", "missing")
    select_builder(page, "transform.else", "parameter")
    fill_builder(page, "transform.else.parameter", "fallback")


def add_evidence(page, file_name, symbol):
    page.locator("#evidence-repository").fill("browser-fixture")
    page.locator("#evidence-commit").fill("e2e-commit")
    page.locator("#evidence-file").fill(file_name)
    page.locator("#evidence-symbol").fill(symbol)
    page.get_by_role("button", name="Add evidence").click()


def assert_literal_guard_roundtrip(page):
    cases = [
        ("string", "123"),
        ("integer", "123"),
        ("decimal", "1.25"),
        ("boolean", True),
        ("null", None),
    ]
    for literal_type, literal_value in cases:
        result = page.evaluate(
            """([literalType, literalValue, sourceNodeID]) => {
                populateEditor({
                    sourceNodeId: sourceNodeID,
                    targetNodeId: sourceNodeID,
                    confidence: 1,
                    guard: {
                        kind: "compare",
                        operator: "eq",
                        left: {kind: "column", nodeId: sourceNodeID},
                        right: {kind: "literal", valueType: literalType, value: literalValue}
                    },
                    transform: {kind: "column_copy", nodeId: sourceNodeID},
                    evidence: []
                });
                return guardFromForm().right;
            }""",
            [literal_type, literal_value, SOURCE_NODE_ID],
        )
        assert result["valueType"] == literal_type, result
        assert result["value"] == literal_value, result


def main():
    console_problems = []
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(headless=True)
        context = browser.new_context(ignore_https_errors=True)
        page = context.new_page()
        page.on(
            "console",
            lambda message: console_problems.append(f"console {message.type}: {message.text}")
            if message.type in {"error", "warning"}
            else None,
        )
        page.on("pageerror", lambda error: console_problems.append(f"pageerror: {error}"))
        try:
            relation_id = run_flow(page)
            run_reviewer_flow(browser, relation_id)
            assert not console_problems, "\n".join(console_problems)
        except Exception:
            ARTIFACTS.mkdir(parents=True, exist_ok=True)
            page.screenshot(path=str(ARTIFACTS / "browser-flow-failure.png"), full_page=True)
            raise
        finally:
            context.close()
            browser.close()


if __name__ == "__main__":
    main()
