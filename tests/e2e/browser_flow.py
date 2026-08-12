"""Browser flow for the dbgraph console.

Drives the console the way an operator does: sign in, create a project, land on
its data sources, add one with a stored connection string, and confirm the
credential never reaches the page. Run by TestBrowserConsoleBootstrapsAProject.
"""

import os
import sys

from playwright.sync_api import expect, sync_playwright

BASE_URL = os.environ["DBGRAPH_BROWSER_BASE_URL"]
TOKEN = os.environ["DBGRAPH_BROWSER_TOKEN"]
PROJECT_ID = os.environ["DBGRAPH_BROWSER_PROJECT_ID"]
ARTIFACTS = os.environ.get("DBGRAPH_BROWSER_ARTIFACTS", ".")

SECRET_PASSWORD = "BrowserFlowSecretPassword"
CONNECTION = f"root:{SECRET_PASSWORD}@tcp(127.0.0.1:3306)/browser_flow?charset=utf8mb4"

failures: list[str] = []


def main() -> int:
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch()
        context = browser.new_context(ignore_https_errors=True, viewport={"width": 1440, "height": 900})
        page = context.new_page()
        page.on("pageerror", lambda error: failures.append(f"page error: {error}"))
        page.on(
            "console",
            lambda message: failures.append(f"console error: {message.text}")
            if message.type == "console" and message.type == "error"
            else None,
        )

        # The console owns the root now.
        page.goto(BASE_URL, wait_until="networkidle")
        expect(page).to_have_url(f"{BASE_URL}/app/login")

        page.locator("#token input").fill(TOKEN)
        page.get_by_role("button", name="Sign in").click()
        page.wait_for_url("**/app/projects", timeout=15000)

        # The seeded project is listed and reachable.
        expect(page.locator("table")).to_contain_text(PROJECT_ID)

        page.get_by_role("button", name="New project").click()
        page.locator("#name").fill("Browser Flow")
        page.locator("#reason").fill("browser end-to-end coverage")
        page.get_by_role("button", name="Create project").click()
        page.wait_for_url("**/data-sources", timeout=15000)

        # Data sources is inert until a project is open, and open it now is.
        data_sources_link = page.locator("a.nav-item", has_text="Data sources")
        assert "disabled" not in (data_sources_link.get_attribute("class") or ""), (
            "Data sources stayed disabled with a project open"
        )

        page.get_by_role("button", name="Add data source").click()
        page.locator("#name").fill("browser-source")
        page.locator("#dsn input").fill(CONNECTION)
        page.locator("#environment").fill("BROWSER_FLOW_DSN")
        page.locator("#reason").fill("stored connection string")
        page.get_by_role("button", name="Create data source").click()

        expect(page.locator("table")).to_contain_text("browser-source", timeout=15000)

        # The whole point of sealing: the credential must not survive in the DOM.
        content = page.content()
        if SECRET_PASSWORD in content:
            index = content.find(SECRET_PASSWORD)
            context = content[max(0, index - 300):index + 150].replace("\n", " ")
            raise AssertionError(f"the connection string leaked into the page near: {context}")

        # A deep link carries the open project, so a reload keeps its place.
        deep_link = page.url
        page.goto(deep_link, wait_until="networkidle")
        expect(page).to_have_url(deep_link)
        expect(page.locator("table")).to_contain_text("browser-source")

        page.screenshot(path=os.path.join(ARTIFACTS, "console-data-sources.png"))
        browser.close()

    if failures:
        print("\n".join(failures), file=sys.stderr)
        return 1
    print("console flow passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
