"""Browser flow for the dbgraph console.

Drives the console the way an operator does: sign in, register a data source
with a stored connection string, and confirm the credential never reaches the
page. Run by TestBrowserConsoleManagesDataSources.
"""

import os
import re
import sys

from playwright.sync_api import expect, sync_playwright

BASE_URL = os.environ["DBGRAPH_BROWSER_BASE_URL"]
TOKEN = os.environ["DBGRAPH_BROWSER_TOKEN"]
ARTIFACTS = os.environ.get("DBGRAPH_BROWSER_ARTIFACTS", ".")

SECRET_PASSWORD = "BrowserFlowSecretPassword"
CONNECTION = f"root:{SECRET_PASSWORD}@tcp(127.0.0.1:3306)/browser_flow?charset=utf8mb4"

failures: list[str] = []


def record_console_error(message) -> None:
    if message.type != "error":
        return
    # Loading the protected root before sign-in intentionally probes the
    # session endpoint and redirects on its 401 response.
    if "Failed to load resource" in message.text and "status of 401" in message.text:
        return
    if message.text == "THREE.WebGLRenderer: WebGL disabled by E2E":
        return
    failures.append(f"console error: {message.text}")


def click_graph_relation(page, sphere, label: str) -> None:
    """Finds a WebGL link through its real hover target, then clicks it."""
    bounds = sphere.bounding_box()
    if bounds is None:
        raise AssertionError("the spherical relation graph has no visible bounds")
    tooltip = page.locator(".graph-tooltip").filter(has_text=label)

    # The fixture's first link runs from the top node towards the middle-left
    # node. Sample the projected path plus small offsets for its 3D curvature;
    # unlike one fixed coordinate this remains stable when the canvas resizes.
    for offset in (0, 0.015, -0.015, 0.03, -0.03, 0.045, -0.045):
        for step in range(5, 96, 2):
            progress = step / 100
            x_ratio = 0.5 + (0.28 - 0.5) * progress + offset
            y_ratio = 0.1 + (0.5 - 0.1) * progress + offset / 2
            x = bounds["x"] + bounds["width"] * x_ratio
            y = bounds["y"] + bounds["height"] * y_ratio
            page.mouse.move(x, y)
            if tooltip.count() and tooltip.is_visible():
                page.mouse.click(x, y)
                return
    raise AssertionError(f"could not hit graph relation {label!r}")


def main() -> int:
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch()
        context = browser.new_context(ignore_https_errors=True, viewport={"width": 1440, "height": 900})
        page = context.new_page()
        page.on("pageerror", lambda error: failures.append(f"page error: {error}"))
        page.on("console", record_console_error)

        # The console owns the root now.
        page.goto(BASE_URL, wait_until="networkidle")
        expect(page).to_have_url(f"{BASE_URL}/app/login")

        page.locator("#token input").fill(TOKEN)
        page.get_by_role("button", name="Sign in").click()
        page.wait_for_url("**/app/data-sources", timeout=15000)
        expect(page.locator("table")).to_contain_text("browser-fixture")

        page.get_by_role("button", name="Register data source").click()
        page.locator("#name").fill("browser-source")
        page.locator("#dsn input").fill(CONNECTION)
        page.locator("#environment").fill("BROWSER_FLOW_DSN")
        page.locator("#reason").fill("stored connection string")
        page.get_by_role("button", name="Register", exact=True).click()

        expect(page.locator("table")).to_contain_text("browser-source", timeout=15000)

        # The whole point of sealing: the credential must not survive in the DOM.
        content = page.content()
        if SECRET_PASSWORD in content:
            index = content.find(SECRET_PASSWORD)
            context = content[max(0, index - 300):index + 150].replace("\n", " ")
            raise AssertionError(f"the connection string leaked into the page near: {context}")

        # The workspace URL is stable because it carries no catalog-scope id.
        deep_link = page.url
        page.goto(deep_link, wait_until="networkidle")
        expect(page).to_have_url(deep_link)
        expect(page.locator("table")).to_contain_text("browser-source")

        # Feed the graph view a small, deterministic relation network. The
        # server/API contract has its own integration tests; this browser seam
        # verifies that an operator can orbit and zoom the picture it returns.
        page.route(
            "**/api/v1/data-sources/*/tables**",
            lambda route: route.fulfill(
                json={
                    "success": True,
                    "data": {
                        "tables": [
                            {
                                "id": "901",
                                "name": "schedulekpoint",
                                "qualifiedName": "resource.schedulekpoint",
                            },
                            {"id": "902", "name": "schedule", "qualifiedName": "resource.schedule"},
                            {"id": "903", "name": "kpoint", "qualifiedName": "resource.kpoint"},
                        ],
                        "truncated": False,
                    },
                    "error": None,
                }
            ),
        )
        page.route(
            "**/api/v1/tables/901",
            lambda route: route.fulfill(
                json={
                    "success": True,
                    "data": {
                        "id": "901",
                        "name": "schedulekpoint",
                        "qualifiedName": "resource.schedulekpoint",
                        "comment": "",
                        "columns": [
                            {
                                "id": "911",
                                "name": "ObjectID",
                                "dataType": "bigint unsigned",
                                "nullable": False,
                                "ordinal": 1,
                                "comment": "",
                            },
                            {
                                "id": "912",
                                "name": "ScheduleID",
                                "dataType": "bigint unsigned",
                                "nullable": False,
                                "ordinal": 2,
                                "comment": "",
                            },
                            {
                                "id": "913",
                                "name": "KPointID",
                                "dataType": "bigint",
                                "nullable": False,
                                "ordinal": 3,
                                "comment": "",
                            },
                            {
                                "id": "914",
                                "name": "KPointName",
                                "dataType": "varchar(50)",
                                "nullable": False,
                                "ordinal": 4,
                                "comment": "",
                            },
                        ],
                        "indexes": [
                            {"name": "PRIMARY", "unique": True, "primary": True, "columns": ["ObjectID"]}
                        ],
                    },
                    "error": None,
                }
            ),
        )
        page.route(
            "**/api/v1/data-sources/*/relation-graph",
            lambda route: route.fulfill(
                json={
                    "success": True,
                    "data": {
                        "tables": [
                            {
                                "id": "901",
                                "name": "schedulekpoint",
                                "qualifiedName": "resource.schedulekpoint",
                            },
                            {"id": "902", "name": "schedule", "qualifiedName": "resource.schedule"},
                            {"id": "903", "name": "kpoint", "qualifiedName": "resource.kpoint"},
                        ],
                        "edges": [
                            {
                                "relationId": "801",
                                "sourceTableId": "901",
                                "targetTableId": "902",
                                "sourceColumn": "ScheduleID",
                                "targetColumn": "ObjectID",
                                "conditional": False,
                                "confidence": 0.98,
                                "cardinality": "MANY_TO_ONE",
                            },
                            {
                                "relationId": "802",
                                "sourceTableId": "902",
                                "targetTableId": "903",
                                "sourceColumn": "KPointID",
                                "targetColumn": "ObjectID",
                                "conditional": True,
                                "confidence": 0.91,
                                "cardinality": "MANY_TO_ONE",
                            },
                        ],
                        "truncated": False,
                    },
                    "error": None,
                }
            ),
        )
        page.get_by_role("link", name="Relation graph").click()
        page.wait_for_url("**/app/relation-graph")

        sphere = page.get_by_role("application", name=re.compile("Spherical relation graph"))
        expect(sphere).to_be_visible()
        expect(sphere.locator("canvas")).to_be_visible()
        expect(page.locator("#sphere-instructions")).to_contain_text("Drag to rotate · Scroll to zoom")

        view_status = page.get_by_test_id("graph-view-status")
        expect(view_status).to_contain_text("100%")
        page.get_by_role("button", name="Zoom in").click()
        expect(view_status).to_contain_text("125%")
        # Let the animated camera transition settle before orbiting, otherwise
        # its remaining zoom events could masquerade as drag-induced movement.
        page.wait_for_timeout(250)
        expect(view_status).to_contain_text("125%")

        status_before_orbit = view_status.text_content()
        orientation_before = re.search(r"(-?\d+)° horizontal · (-?\d+)° vertical", status_before_orbit or "")
        if orientation_before is None:
            raise AssertionError(f"unexpected graph view status: {status_before_orbit!r}")
        bounds = sphere.bounding_box()
        if bounds is None:
            raise AssertionError("the spherical relation graph has no visible bounds")
        # OrbitControls rotates on a background drag. Start outside the node
        # sphere so this tests navigation rather than selecting a table.
        page.mouse.move(bounds["x"] + bounds["width"] * 0.08, bounds["y"] + bounds["height"] / 2)
        page.mouse.down()
        page.mouse.move(bounds["x"] + bounds["width"] * 0.28, bounds["y"] + bounds["height"] * 0.4)
        page.mouse.up()
        orientation_after = re.search(
            r"(-?\d+)° horizontal · (-?\d+)° vertical", view_status.text_content() or ""
        )
        if orientation_after is None or orientation_after.groups() == orientation_before.groups():
            raise AssertionError("dragging the spherical graph did not rotate the view")

        page.get_by_role("button", name="Reset graph view").click()
        expect(view_status).to_have_text("100% zoom · 0° horizontal · 0° vertical")

        page.get_by_role("button", name="Show the table list").click()
        page.get_by_role("button", name=re.compile(r"^schedulekpoint")).click()
        table_drawer = page.get_by_role("dialog", name="resource.schedulekpoint")
        expect(table_drawer).to_be_visible()
        expect(table_drawer).to_contain_text("4 columns · 1 index")
        table_tab = table_drawer.get_by_role("tab", name="Table", exact=True)
        expect(table_tab).to_have_attribute("aria-selected", "true")
        expect(table_drawer).to_contain_text("ObjectID")
        expect(table_drawer).to_contain_text("bigint unsigned")

        table_drawer.get_by_role("tab", name="Index", exact=True).click()
        primary_index = table_drawer.get_by_role("listitem").filter(has_text="PRIMARY")
        expect(primary_index).to_contain_text("primary")
        expect(primary_index).to_contain_text("ObjectID")
        expect(page.locator("section.canvas")).not_to_contain_text("Columns")

        table_drawer.get_by_role("tab", name="Relations", exact=True).click()
        expect(table_drawer).to_contain_text("1 approved relation touching this table")
        expect(table_drawer.get_by_role("button", name=re.compile(r"^Relations"))).to_have_count(0)
        selected_relation = table_drawer.get_by_role("button", name=re.compile(r"schedulekpoint\.ScheduleID"))
        selected_relation.click()
        expect(table_drawer).not_to_be_visible()
        expect(sphere).to_be_focused()
        expect(sphere).to_have_attribute("aria-label", re.compile(r"Selected relation 801"))
        expect(page.get_by_test_id("graph-accessible-status")).to_contain_text(
            "Selected relation schedulekpoint.ScheduleID → schedule.ObjectID"
        )

        click_graph_relation(page, sphere, "schedulekpoint.ScheduleID → schedule.ObjectID")
        expect(table_drawer).to_be_visible()
        expect(table_drawer.get_by_role("tab", name="Relations", exact=True)).to_have_attribute(
            "aria-selected", "true"
        )
        selected_relation = table_drawer.get_by_role("button", name=re.compile(r"schedulekpoint\.ScheduleID"))
        expect(selected_relation).to_have_attribute("aria-pressed", "true")
        selected_relation_details = table_drawer.get_by_role("region", name="Selected relation details")
        expect(selected_relation_details).to_contain_text("Many to one: ScheduleID repeats, ObjectID is unique")
        expect(selected_relation_details).to_contain_text("Relation ID")
        expect(selected_relation_details).to_contain_text("801")

        page.keyboard.press("Escape")
        expect(table_drawer).not_to_be_visible()
        expect(page.get_by_role("region", name="Selected relation details")).to_have_count(0)
        expect(page.locator("section.canvas")).not_to_contain_text("Cardinality")
        expect(page.locator("section.canvas")).not_to_contain_text("confident")

        # If WebGL cannot initialize, selecting a relation still closes the
        # drawer and must restore focus to a visible fallback, not the hidden
        # canvas host.
        page.locator('a[href="/app/data-sources"]').click()
        page.evaluate(
            """
            () => {
              const original = HTMLCanvasElement.prototype.getContext;
              HTMLCanvasElement.prototype.getContext = function(type, ...args) {
                if (type === "webgl" || type === "webgl2") throw new Error("WebGL disabled by E2E");
                return original.call(this, type, ...args);
              };
            }
            """
        )
        page.locator('a[href="/app/relation-graph"]').click()
        fallback = page.get_by_role("alert").filter(has_text="The 3D graph is unavailable")
        expect(fallback).to_be_visible()
        page.get_by_role("button", name="Show the table list").click()
        page.get_by_role("button", name=re.compile(r"^schedulekpoint")).click()
        table_drawer = page.get_by_role("dialog", name="resource.schedulekpoint")
        table_drawer.get_by_role("tab", name="Relations", exact=True).click()
        table_drawer.get_by_role("button", name=re.compile(r"schedulekpoint\.ScheduleID")).click()
        expect(table_drawer).not_to_be_visible()
        expect(fallback).to_be_focused()

        # A source switch must retire the old interactive graph before the new
        # response arrives (or fails), so an old edge cannot repopulate the new
        # source's drawer state.
        graph_host = page.locator("section.canvas .sphere")
        expect(graph_host).to_have_count(1)
        page.unroute("**/api/v1/data-sources/*/relation-graph")
        page.route(
            "**/api/v1/data-sources/*/relation-graph",
            lambda route: route.fulfill(
                json={
                    "success": False,
                    "data": None,
                    "error": {"code": "FIXTURE_UNAVAILABLE", "message": "fixture response unavailable"},
                },
            ),
        )
        page.locator(".source-picker").click()
        page.get_by_role("option", name="browser-source", exact=True).click()
        expect(graph_host).to_have_count(0)

        page.screenshot(path=os.path.join(ARTIFACTS, "console-relation-sphere.png"))
        browser.close()

    if failures:
        print("\n".join(failures), file=sys.stderr)
        return 1
    print("console flow passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
