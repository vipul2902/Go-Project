// Captures dashboard screenshots for the docs by driving a real browser
// through the real UI (login form included). Uses the locally installed
// Chrome via playwright-core, so no browser download is needed.
//
//   BASE_URL=http://localhost:8080 EMAIL=admin@clouddeploy.local PASSWORD=... node scripts/screenshots.mjs
import { fileURLToPath } from "node:url";
import { chromium } from "playwright-core";

const base = process.env.BASE_URL ?? "http://localhost:8080";
// fileURLToPath, not URL.pathname: the latter keeps "%20" for spaces in paths.
const out = fileURLToPath(new URL("../../docs/screenshots/", import.meta.url));
const { EMAIL, PASSWORD } = process.env;
if (!EMAIL || !PASSWORD) throw new Error("EMAIL and PASSWORD are required");

const browser = await chromium.launch({ channel: "chrome", headless: true });
const page = await browser.newPage({ viewport: { width: 1360, height: 860 }, colorScheme: process.env.SCHEME ?? "light" });
const errors = [];
// Fail on uncaught exceptions and on unexpected HTTP errors. The one 401
// expected is the silent session-restore attempt before signing in.
page.on("pageerror", (e) => errors.push(`exception: ${e.message}`));
page.on("response", (r) => {
  const expected = r.status() === 401 && r.url().endsWith("/api/v1/auth/refresh");
  if (r.status() >= 400 && !expected) errors.push(`HTTP ${r.status()} ${r.request().method()} ${r.url()}`);
});

const shot = async (name) => {
  await page.waitForLoadState("networkidle").catch(() => {});
  await page.waitForTimeout(600);
  await page.screenshot({ path: `${out}${name}.png` });
  console.log("captured", name);
};

await page.goto(`${base}/login`);
await page.getByLabel("Email").fill(EMAIL);
await page.getByLabel("Password").fill(PASSWORD);
await page.getByRole("button", { name: "Sign in" }).click();
await page.getByRole("heading", { name: "Dashboard" }).waitFor();
await shot("dashboard");

await page.getByRole("link", { name: "Projects", exact: true }).click();
await page.getByRole("heading", { name: "Projects" }).waitFor();
await shot("projects");

await page.getByRole("link", { name: /hello-world/ }).first().click();
await page.getByRole("heading", { name: "Recent deployments" }).waitFor();
await page.waitForTimeout(1500); // health cards poll the cluster
await shot("project-overview");

await page.getByRole("link", { name: "Environments" }).click();
await page.getByRole("heading", { name: "Variables" }).first().waitFor();
await shot("environments");

await page.getByRole("link", { name: "Deployments", exact: true }).click();
await page.getByRole("heading", { name: "Deployment history" }).waitFor();
await page.locator("table a.mono").first().click();
await page.getByRole("heading", { name: "Pipeline log" }).waitFor();
await page.waitForTimeout(2500); // let the log stream in
await shot("deployment");

await page.getByRole("button", { name: "Roll back to this" }).click().catch(() => {});
if (await page.getByRole("dialog").isVisible().catch(() => false)) {
  await shot("rollback-confirm");
  await page.getByRole("button", { name: "Cancel" }).click();
}

await page.getByRole("link", { name: "Audit log" }).click();
await page.getByRole("heading", { name: "Audit log" }).waitFor();
await shot("audit");

await page.getByRole("link", { name: "Infrastructure" }).click();
await page.getByRole("heading", { name: "Infrastructure" }).waitFor();
await page.waitForTimeout(1500);
await shot("infrastructure");

await browser.close();
if (errors.length) {
  console.error("browser errors:\n" + errors.join("\n"));
  process.exit(1);
}
console.log("no browser console errors");
