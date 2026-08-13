"""Browser flow for the dbgraph console.

Drives the console the way an operator does: sign in, register a data source
with a stored connection string, and confirm the credential never reaches the
page. Run by TestBrowserConsoleManagesDataSources.
"""

import os
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
    failures.append(f"console error: {message.text}")


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

        page.screenshot(path=os.path.join(ARTIFACTS, "console-data-sources.png"))
        browser.close()

    if failures:
        print("\n".join(failures), file=sys.stderr)
        return 1
    print("console flow passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
