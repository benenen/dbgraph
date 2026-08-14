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
expected_console_errors: list[str] = []


def record_console_error(message) -> None:
    if message.type != "error":
        return
    # Loading the protected root before sign-in intentionally probes the
    # session endpoint and redirects on its 401 response.
    if "Failed to load resource" in message.text and "status of 401" in message.text:
        return
    if message.text == "THREE.WebGLRenderer: WebGL disabled by E2E":
        return
    for expected in expected_console_errors:
        if expected in message.text:
            expected_console_errors.remove(expected)
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


def review_proposal(
    relation_id: str,
    revision_no: int,
    proposal_kind: str,
    source_node_id: str,
    target_node_id: str,
) -> dict:
    revision = {
        "id": f"revision-{relation_id}",
        "relationId": relation_id,
        "revisionNo": revision_no,
        "kind": proposal_kind,
        "sourceNodeId": source_node_id,
        "targetNodeId": target_node_id,
        "guard": None,
        "selector": None,
        "transform": {"kind": "column_copy", "nodeId": source_node_id},
        "confidence": 0.9,
        "evidence": [
            {
                "kind": "CODE",
                "repository": "browser-fixture",
                "commit": "abcdef1234567890",
                "file": "Mapper.java",
                "symbol": "map",
                "startLine": 10,
                "endLine": 12,
            }
        ],
        "actor": "browser-agent",
        "reason": "Browser bulk review fixture",
        "requestId": f"request-{relation_id}",
        "createdAt": "2026-08-14T00:00:00Z",
    }
    return {
        "id": relation_id,
        "type": "CONDITIONAL_VALUE_COPY",
        "latestRevisionNo": revision_no,
        "status": "PENDING",
        "effective": False,
        "active": None,
        "proposed": revision,
        "createdAt": "2026-08-14T00:00:00Z",
    }


def wait_for_review_calls(page, calls: list, expected_count: int) -> None:
    for _ in range(100):
        page.wait_for_timeout(25)
        if len(calls) >= expected_count:
            return
    raise AssertionError(f"expected {expected_count} review calls, got {len(calls)}")


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

        # Bulk review is a bounded, confirmed sequence. Hold every POST so the
        # browser can prove that no second mutation or refresh is possible
        # while one optimistic-concurrency decision is in flight.
        review_proposals = [
            review_proposal("501", 7, "CONTENT", "601", "602"),
            review_proposal("502", 3, "TOMBSTONE", "603", "604"),
            review_proposal("503", 11, "STALE", "601", "604"),
        ]
        node_names = {
            "601": "resource.schedulekpoint.ScheduleID",
            "602": "resource.schedule.ObjectID",
            "603": "resource.schedulekpoint.KPointID",
            "604": "resource.knowledgepoint.ObjectID",
        }
        review_calls = []
        held_reviews = []
        held_proposal_reloads = []
        held_navigation_reloads = []
        hold_next_proposal_reload = {"value": True}
        hold_navigation_completion_reload = {"value": True}
        navigation_first_completed = {"value": False}
        navigation_proposals = [
            review_proposal("503", 12, "STALE", "601", "604"),
            review_proposal("504", 1, "CONTENT", "603", "602"),
        ]

        def fulfill_proposals(route) -> None:
            relations = review_proposals
            truncated = True
            if len(review_calls) >= 3:
                relations = [
                    review_proposal("501", 8, "CONTENT", "601", "602"),
                    review_proposal("502", 4, "TOMBSTONE", "603", "604"),
                ]
                truncated = False
            if len(review_calls) >= 5:
                if hold_next_proposal_reload["value"]:
                    hold_next_proposal_reload["value"] = False
                    held_proposal_reloads.append(route)
                    return
                if (
                    navigation_first_completed["value"]
                    and hold_navigation_completion_reload["value"]
                ):
                    hold_navigation_completion_reload["value"] = False
                    held_navigation_reloads.append(route)
                    return
                relations = (
                    navigation_proposals[1:]
                    if navigation_first_completed["value"]
                    else navigation_proposals
                )
            route.fulfill(
                json={
                    "success": True,
                    "data": {"relations": relations, "truncated": truncated},
                    "error": None,
                }
            )

        page.route("**/api/v1/relation-proposals", fulfill_proposals)
        page.route(
            "**/api/v1/nodes/*",
            lambda route: route.fulfill(
                json={
                    "success": True,
                    "data": {
                        "id": route.request.url.rsplit("/", 1)[-1],
                        "name": node_names[route.request.url.rsplit("/", 1)[-1]].rsplit(".", 1)[-1],
                        "qualifiedName": node_names[route.request.url.rsplit("/", 1)[-1]],
                        "kind": "COLUMN",
                        "dataType": "bigint",
                    },
                    "error": None,
                }
            ),
        )

        def hold_review(route) -> None:
            review_calls.append((route.request.url, route.request.post_data_json))
            held_reviews.append(route)

        page.route("**/api/v1/relations/*/reviews", hold_review)
        page.get_by_role("link", name="Review", exact=True).click()
        expect(
            page.get_by_text(
                "3 waiting on this page — decide these to see the rest",
                exact=True,
            )
        ).to_be_visible()

        content_card = page.locator("li.proposal").filter(
            has_text="resource.schedulekpoint.ScheduleID"
        ).filter(has_text="resource.schedule.ObjectID")
        tombstone_card = page.locator("li.proposal").filter(
            has_text="resource.schedulekpoint.KPointID"
        )
        stale_card = page.locator("li.proposal").filter(
            has_text="resource.knowledgepoint.ObjectID"
        ).filter(has_text="resource.schedulekpoint.ScheduleID")
        expect(content_card).to_contain_text("content update")
        expect(tombstone_card).to_contain_text("tombstone — removes relation")
        expect(stale_card).to_contain_text("stale — removes relation")
        content_card.locator("textarea").fill("reason one")
        tombstone_card.locator("textarea").fill("reason two")

        page.get_by_role("button", name="Approve all 3").click()
        confirmation = page.get_by_role(
            "alertdialog", name="Approve 3 proposals, including 2 removals?"
        )
        expect(confirmation).to_contain_text(
            "1 content update, 1 tombstone, and 1 stale candidate"
        )
        expect(confirmation).to_contain_text(
            "Approving the 2 removal proposals removes their relations from the effective graph."
        )
        expect(confirmation).to_contain_text(
            "Only the proposals on this page are decided; more are waiting."
        )
        page.go_back()
        page.wait_for_url("**/app/data-sources")
        expect(confirmation).not_to_be_visible()
        page.wait_for_timeout(150)
        if review_calls:
            raise AssertionError(
                f"leaving an open bulk confirmation sent requests: {review_calls!r}"
            )
        page.get_by_role("link", name="Review", exact=True).click()
        expect(
            page.get_by_text(
                "3 waiting on this page — decide these to see the rest",
                exact=True,
            )
        ).to_be_visible()
        content_card.locator("textarea").fill("reason one")
        tombstone_card.locator("textarea").fill("reason two")
        page.get_by_role("button", name="Approve all 3").click()
        confirmation = page.get_by_role(
            "alertdialog", name="Approve 3 proposals, including 2 removals?"
        )
        confirmation.get_by_role("button", name="Cancel").click()
        expect(confirmation).not_to_be_visible()
        if review_calls:
            raise AssertionError(f"canceling bulk review still sent requests: {review_calls!r}")

        page.get_by_role("button", name="Approve all 3").click()
        destructive_accept = confirmation.get_by_role("button", name="Approve 3, remove 2")
        expect(destructive_accept).to_have_class(re.compile(r"p-button-danger"))
        destructive_accept.click()
        bulk_progress = page.locator(".bulk-progress").filter(has_text="Approving 0 / 3")
        expect(bulk_progress).to_be_visible()
        expect(bulk_progress).to_be_focused()
        wait_for_review_calls(page, review_calls, 1)
        page.wait_for_timeout(150)
        if len(review_calls) != 1:
            raise AssertionError(f"bulk review ran requests concurrently: {review_calls!r}")

        expect(page.get_by_role("button", name="Refresh")).to_be_disabled()
        row_actions = page.get_by_role("button", name=re.compile(r"^(Approve|Reject)$"))
        if row_actions.count() != 6:
            raise AssertionError(f"expected 6 per-proposal actions, got {row_actions.count()}")
        for index in range(row_actions.count()):
            expect(row_actions.nth(index)).to_be_disabled()
        row_reasons = page.locator("li.proposal textarea")
        for index in range(row_reasons.count()):
            expect(row_reasons.nth(index)).to_be_disabled()

        held_reviews[0].fulfill(json={"success": True, "data": {}, "error": None})
        wait_for_review_calls(page, review_calls, 2)
        expected_console_errors.append("status of 409")
        held_reviews[1].fulfill(
            status=409,
            json={
                "success": False,
                "data": None,
                "error": {"code": "REVISION_CONFLICT", "message": "proposal changed"},
            },
        )
        wait_for_review_calls(page, review_calls, 3)
        held_reviews[2].fulfill(json={"success": True, "data": {}, "error": None})

        expected_reviews = [
            ("501", 7, "APPROVE", "reason one"),
            ("502", 3, "APPROVE", "reason two"),
            ("503", 11, "APPROVE", "Approved in bulk from the review queue"),
        ]
        actual_reviews = [
            (
                url.rsplit("/", 2)[-2],
                body["expectedRevisionNo"],
                body["decision"],
                body["reason"],
            )
            for url, body in review_calls
        ]
        if actual_reviews != expected_reviews:
            raise AssertionError(f"review sequence = {actual_reviews!r}")
        result_toast = page.get_by_role("alert").filter(has_text="2 of 3 approved")
        expect(result_toast).to_be_visible()
        expect(result_toast).to_contain_text("proposal changed")
        expect(page.get_by_text("2 waiting", exact=True)).to_be_visible()
        expect(page.locator(".bulk-bar")).to_be_focused()
        refreshed_content_card = page.locator("li.proposal").filter(
            has_text="resource.schedulekpoint.ScheduleID"
        ).filter(has_text="resource.schedule.ObjectID")
        expect(refreshed_content_card).to_contain_text("revision 8")
        expect(refreshed_content_card.locator("textarea")).to_have_value("")
        expect(tombstone_card).to_contain_text("revision 4")
        expect(tombstone_card.locator("textarea")).to_have_value("")

        # When every item on a truncated page succeeds, the live batch status
        # remains mounted until the next bounded page arrives. It must never
        # claim the whole queue is empty between the last POST and that reload.
        page.get_by_role("button", name="Reject all 2").click()
        reject_confirmation = page.get_by_role("alertdialog", name="Reject 2 proposals?")
        expect(reject_confirmation).to_contain_text("1 content update and 1 tombstone")
        expect(reject_confirmation).to_contain_text("The effective graph does not change.")
        reject_confirmation.get_by_role("button", name="Reject 2").click()
        wait_for_review_calls(page, review_calls, 4)
        held_reviews[3].fulfill(json={"success": True, "data": {}, "error": None})
        wait_for_review_calls(page, review_calls, 5)
        held_reviews[4].fulfill(json={"success": True, "data": {}, "error": None})
        wait_for_review_calls(page, held_proposal_reloads, 1)
        completed_progress = page.locator(".bulk-progress").filter(has_text="Rejecting 2 / 2")
        expect(completed_progress).to_be_visible()
        expect(completed_progress).to_be_focused()
        expect(page.get_by_text("Nothing waiting", exact=False)).to_have_count(0)
        held_proposal_reloads[0].fulfill(
            json={
                "success": True,
                "data": {"relations": navigation_proposals, "truncated": False},
                "error": None,
            }
        )
        expect(page.get_by_text("2 waiting", exact=True)).to_be_visible()
        expect(page.locator(".bulk-bar")).to_be_focused()

        # Leaving during an in-flight batch cancels the old instance before it
        # starts the next POST. A new Review instance observes the shared lock,
        # waits for that request, then refreshes the authoritative queue.
        page.get_by_role("button", name="Reject all 2").click()
        page.get_by_role("alertdialog", name="Reject 2 proposals?").get_by_role(
            "button", name="Reject 2"
        ).click()
        wait_for_review_calls(page, review_calls, 6)
        page.get_by_role("link", name="Data sources", exact=True).click()
        page.get_by_role("link", name="Review", exact=True).click()
        expect(page.get_by_text("A review operation is finishing", exact=False)).to_be_visible()
        expect(page.get_by_role("button", name="Reject all 2")).to_be_disabled()
        navigation_first_completed["value"] = True
        held_reviews[5].fulfill(json={"success": True, "data": {}, "error": None})
        wait_for_review_calls(page, held_navigation_reloads, 1)
        expect(
            page.get_by_text(
                "Refreshing the review queue before controls are unlocked.",
                exact=True,
            )
        ).to_be_visible()
        expect(page.get_by_role("button", name="Reject all 2")).to_be_disabled()
        expect(page.get_by_role("button", name="Refresh")).to_be_disabled()
        page.wait_for_timeout(150)
        if len(review_calls) != 6:
            raise AssertionError(f"an unmounted bulk review continued: {review_calls!r}")
        held_navigation_reloads[0].fulfill(
            json={
                "success": True,
                "data": {"relations": navigation_proposals[1:], "truncated": False},
                "error": None,
            }
        )
        expect(page.get_by_text("1 waiting", exact=True)).to_be_visible()
        expect(page.get_by_role("button", name="Reject all 1")).to_be_enabled()

        page.unroute("**/api/v1/relation-proposals")
        page.unroute("**/api/v1/nodes/*")
        page.unroute("**/api/v1/relations/*/reviews")

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
                                "comment": "The knowledge point name shown to teachers when they arrange a learning schedule.",
                            },
                        ],
                        "indexes": [
                            {"name": "PRIMARY", "unique": True, "primary": True, "columns": ["ObjectID"]},
                            {
                                "name": "idx_schedule_kpoint",
                                "unique": False,
                                "primary": False,
                                "columns": ["ScheduleID", "KPointID"],
                            },
                        ],
                    },
                    "error": None,
                }
            ),
        )
        page.route(
            "**/api/v1/tables/902",
            lambda route: route.fulfill(
                json={
                    "success": True,
                    "data": {
                        "id": "902",
                        "name": "schedule",
                        "qualifiedName": "resource.schedule",
                        "comment": "",
                        "columns": [
                            {
                                "id": "921",
                                "name": "ObjectID",
                                "dataType": "bigint unsigned",
                                "nullable": False,
                                "ordinal": 1,
                                "comment": "",
                            },
                            {
                                "id": "922",
                                "name": "KPointID",
                                "dataType": "bigint",
                                "nullable": False,
                                "ordinal": 2,
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
                                "sourceTableId": "901",
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

        primary_navigation = page.get_by_role("navigation", name="Primary")
        collapse_navigation = page.get_by_role("button", name="Collapse navigation")
        expanded_navigation_width = primary_navigation.bounding_box()
        if expanded_navigation_width is None:
            raise AssertionError("the primary navigation has no visible bounds")
        for label in ("Data sources", "Relation graph", "Review"):
            expect(primary_navigation.get_by_text(label, exact=True)).to_be_visible()
        collapse_navigation.click()
        expect(page.get_by_role("button", name="Expand navigation")).to_be_visible()
        page.wait_for_function(
            """threshold => {
              const nav = document.querySelector('nav[aria-label="Primary"]');
              return nav && nav.getBoundingClientRect().width < threshold;
            }""",
            arg=expanded_navigation_width["width"] * 0.6,
        )
        collapsed_navigation_width = primary_navigation.bounding_box()
        if collapsed_navigation_width is None:
            raise AssertionError("the collapsed primary navigation has no visible bounds")
        for label in ("Data sources", "Relation graph", "Review"):
            link = primary_navigation.get_by_role("link", name=label, exact=True)
            expect(link).to_be_visible()
            expect(link).to_have_accessible_name(label)
            expect(link.get_by_text(label, exact=True)).to_be_hidden()
        page.get_by_role("button", name="Expand navigation").click()
        page.wait_for_function(
            """threshold => {
              const nav = document.querySelector('nav[aria-label="Primary"]');
              return nav && nav.getBoundingClientRect().width > threshold;
            }""",
            arg=expanded_navigation_width["width"] * 0.9,
        )
        for label in ("Data sources", "Relation graph", "Review"):
            expect(primary_navigation.get_by_text(label, exact=True)).to_be_visible()

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
        table_drawer = page.get_by_role("complementary", name="resource.schedulekpoint")
        expect(table_drawer).to_be_visible()
        expect(table_drawer).to_be_focused()
        expect(table_drawer).not_to_have_attribute("aria-modal", "true")
        expect(page.locator(".p-overlay-mask")).to_have_count(0)
        close_drawer = table_drawer.get_by_role("button", name="Close")
        close_drawer.focus()
        page.keyboard.press("Shift+Tab")
        expect(sphere).to_be_focused()
        drawer_bounds = table_drawer.bounding_box()
        if drawer_bounds is None or drawer_bounds["width"] > 560:
            raise AssertionError(f"the table drawer is still too wide: {drawer_bounds!r}")
        page.locator("section.canvas").click(position={"x": 20, "y": 20})
        expect(table_drawer).to_be_visible()
        page.keyboard.press("Escape")
        expect(table_drawer).to_be_visible()
        page.get_by_role("button", name=re.compile(r"^schedulekpoint")).click()
        expect(table_drawer).to_be_visible()
        expect(table_drawer).to_contain_text("4 columns · 2 indexes")
        table_tab = table_drawer.get_by_role("tab", name="Table", exact=True)
        expect(table_tab).to_have_attribute("aria-selected", "true")
        expect(table_drawer).to_contain_text("ObjectID")
        expect(table_drawer).to_contain_text("bigint unsigned")
        columns_table = table_drawer.get_by_role("table", name="Table columns")
        for header in ("Name", "Type", "Constraint", "Comment"):
            expect(columns_table.get_by_role("columnheader", name=header, exact=True)).to_be_visible()
        long_comment = columns_table.get_by_role(
            "cell",
            name="The knowledge point name shown to teachers when they arrange a learning schedule.",
        )
        comment_bounds = long_comment.bounding_box()
        if comment_bounds is None or comment_bounds["height"] < 40:
            raise AssertionError(f"the long column comment did not wrap: {comment_bounds!r}")
        page.set_viewport_size({"width": 320, "height": 800})
        expect(table_drawer).to_be_visible()
        narrow_drawer_bounds = table_drawer.bounding_box()
        if narrow_drawer_bounds is None or narrow_drawer_bounds["width"] > 320:
            raise AssertionError(f"the drawer overflows a narrow viewport: {narrow_drawer_bounds!r}")
        overflow = page.evaluate(
            """() => ({
              page: `${document.documentElement.scrollWidth}/${window.innerWidth}`,
              elements: [...document.querySelectorAll('body *')]
                .map((element) => {
                  const rect = element.getBoundingClientRect();
                  return { tag: element.tagName, className: element.className, left: rect.left, right: rect.right };
                })
                .filter((item) => item.left < -1 || item.right > window.innerWidth + 1)
                .slice(0, 12),
            })"""
        )
        if overflow["page"] != "320/320":
            raise AssertionError(f"the application overflows the narrow viewport: {overflow!r}")
        fields_scroll = table_drawer.locator(".fields-scroll")
        scroll_widths = fields_scroll.evaluate(
            "element => ({ clientWidth: element.clientWidth, scrollWidth: element.scrollWidth })"
        )
        if scroll_widths["scrollWidth"] <= scroll_widths["clientWidth"]:
            raise AssertionError(f"the narrow columns table has no local horizontal scroll area: {scroll_widths!r}")
        page.set_viewport_size({"width": 1440, "height": 900})
        column_filter = table_drawer.get_by_role("textbox", name="Filter columns")
        column_filter.fill("KPointName")
        expect(columns_table.get_by_role("cell", name="KPointName", exact=True)).to_be_visible()
        expect(columns_table.get_by_role("cell", name="ObjectID", exact=True)).to_have_count(0)

        table_drawer.get_by_role("tab", name="Index", exact=True).click()
        primary_index = table_drawer.get_by_role("listitem").filter(has_text="PRIMARY")
        expect(primary_index).to_contain_text("primary")
        expect(primary_index).to_contain_text("ObjectID")
        index_filter = table_drawer.get_by_role("textbox", name="Filter indexes")
        index_filter.fill("KPointID")
        expect(table_drawer.get_by_role("listitem").filter(has_text="idx_schedule_kpoint")).to_be_visible()
        expect(primary_index).to_have_count(0)
        expect(page.locator("section.canvas")).not_to_contain_text("Columns")

        table_drawer.get_by_role("tab", name="Relations", exact=True).click()
        expect(table_drawer).to_contain_text("2 approved relations touching this table")
        expect(table_drawer.get_by_role("button", name=re.compile(r"^Relations"))).to_have_count(0)
        relation_filter = table_drawer.get_by_role("textbox", name="Filter relations")
        relation_filter.fill("KPointID")
        expect(
            table_drawer.get_by_role("button", name=re.compile(r"schedulekpoint\.KPointID"))
        ).to_be_visible()
        expect(
            table_drawer.get_by_role("button", name=re.compile(r"schedulekpoint\.ScheduleID"))
        ).to_have_count(0)
        relation_filter.fill("ScheduleID")
        selected_relation = table_drawer.get_by_role("button", name=re.compile(r"schedulekpoint\.ScheduleID"))
        selected_relation.click()
        expect(table_drawer).to_be_visible()
        expect(selected_relation).to_have_attribute("aria-pressed", "true")
        selected_relation.click()
        expect(table_drawer).to_be_visible()
        expect(selected_relation).to_have_attribute("aria-pressed", "true")
        expect(sphere).to_have_attribute("aria-label", re.compile(r"Selected relation 801"))
        expect(page.get_by_test_id("graph-accessible-status")).to_contain_text(
            "Selected relation schedulekpoint.ScheduleID → schedule.ObjectID"
        )
        relation_filter.fill("KPointID")
        table_drawer.get_by_role("button", name="Close").click()
        expect(table_drawer).not_to_be_visible()
        expect(sphere).to_be_focused()

        click_graph_relation(page, sphere, "schedulekpoint.ScheduleID → schedule.ObjectID")
        expect(table_drawer).to_be_visible()
        expect(table_drawer.get_by_role("tab", name="Relations", exact=True)).to_have_attribute(
            "aria-selected", "true"
        )
        expect(table_drawer.get_by_role("textbox", name="Filter relations")).to_have_value("")
        selected_relation = table_drawer.get_by_role("button", name=re.compile(r"schedulekpoint\.ScheduleID"))
        expect(selected_relation).to_have_attribute("aria-pressed", "true")
        selected_relation_details = table_drawer.get_by_role("region", name="Selected relation details")
        expect(selected_relation_details).to_contain_text("Many to one: ScheduleID repeats, ObjectID is unique")
        expect(selected_relation_details).to_contain_text("Relation ID")
        expect(selected_relation_details).to_contain_text("801")

        # Switching tables resets all three filters for the new table context.
        table_drawer.get_by_role("textbox", name="Filter relations").fill("ScheduleID")
        table_list = page.locator(".table-list")
        table_list.get_by_role("button", name=re.compile(r"^schedule(?: in the graph)?$")).click()
        expect(page.get_by_role("complementary", name="resource.schedule")).to_be_visible()
        table_list.get_by_role("button", name=re.compile(r"^schedulekpoint")).click()
        table_drawer = page.get_by_role("complementary", name="resource.schedulekpoint")
        expect(table_drawer.get_by_role("textbox", name="Filter columns")).to_have_value("")
        table_drawer.get_by_role("tab", name="Index", exact=True).click()
        expect(table_drawer.get_by_role("textbox", name="Filter indexes")).to_have_value("")
        table_drawer.get_by_role("tab", name="Relations", exact=True).click()
        expect(table_drawer.get_by_role("textbox", name="Filter relations")).to_have_value("")

        # A data-source change also clears every drawer filter. Edge-to-drawer
        # selection is already exercised above; use the stable table-list seam
        # here so this state-reset assertion does not depend on a second 3D hit.
        table_drawer.get_by_role("tab", name="Table", exact=True).click()
        table_drawer.get_by_role("textbox", name="Filter columns").fill("ObjectID")
        table_drawer.get_by_role("tab", name="Index", exact=True).click()
        table_drawer.get_by_role("textbox", name="Filter indexes").fill("PRIMARY")
        table_drawer.get_by_role("tab", name="Relations", exact=True).click()
        table_drawer.get_by_role("textbox", name="Filter relations").fill("KPointID")
        table_drawer.get_by_role("button", name="Close").click()
        expect(table_drawer).not_to_be_visible()
        page.locator(".source-picker").click()
        with page.expect_response("**/api/v1/data-sources/*/relation-graph") as source_graph_response:
            page.get_by_role("option", name="browser-source", exact=True).click()
        source_graph_response.value.finished()
        table_list.get_by_role("button", name=re.compile(r"^schedulekpoint")).click()
        table_drawer = page.get_by_role("complementary", name="resource.schedulekpoint")
        expect(table_drawer.get_by_role("textbox", name="Filter columns")).to_have_value("")
        table_drawer.get_by_role("tab", name="Index", exact=True).click()
        expect(table_drawer.get_by_role("textbox", name="Filter indexes")).to_have_value("")
        table_drawer.get_by_role("tab", name="Relations", exact=True).click()
        expect(table_drawer.get_by_role("textbox", name="Filter relations")).to_have_value("")

        table_drawer.get_by_role("button", name="Close").click()
        expect(table_drawer).not_to_be_visible()
        expect(page.get_by_role("region", name="Selected relation details")).to_have_count(0)
        expect(page.locator("section.canvas")).not_to_contain_text("Cardinality")
        expect(page.locator("section.canvas")).not_to_contain_text("confident")

        # If WebGL cannot initialize, explicitly closing the drawer must restore
        # focus to a visible fallback, not the hidden canvas host.
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
        table_drawer = page.get_by_role("complementary", name="resource.schedulekpoint")
        table_drawer.get_by_role("tab", name="Relations", exact=True).click()
        table_drawer.get_by_role("button", name=re.compile(r"schedulekpoint\.ScheduleID")).click()
        expect(table_drawer).to_be_visible()
        table_drawer.get_by_role("button", name="Close").click()
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

        # A source with tables but no relations has no RelationSphere focus
        # target. Closing table details returns to the table that opened them.
        page.unroute("**/api/v1/data-sources/*/relation-graph")
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
                            }
                        ],
                        "edges": [],
                        "truncated": False,
                    },
                    "error": None,
                },
            ),
        )
        page.locator(".source-picker").click()
        page.get_by_role("option", name="browser-fixture", exact=True).click()
        expect(page.get_by_text("No relations yet", exact=True)).to_be_visible()
        table_opener = table_list.get_by_role("button", name=re.compile(r"^schedulekpoint"))
        table_opener.click()
        table_drawer = page.get_by_role("complementary", name="resource.schedulekpoint")
        expect(table_drawer).to_be_focused()
        table_drawer.get_by_role("button", name="Close").click()
        expect(table_drawer).not_to_be_visible()
        expect(table_opener).to_be_focused()

        page.screenshot(path=os.path.join(ARTIFACTS, "console-relation-sphere.png"))
        browser.close()

    if expected_console_errors:
        failures.append(f"expected console errors were not observed: {expected_console_errors!r}")
    if failures:
        print("\n".join(failures), file=sys.stderr)
        return 1
    print("console flow passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
