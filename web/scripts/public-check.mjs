// Post-deploy smoke against a deployed, path-routed host (default the
// public one): the root app must be LIVE (no demo data), degrade cleanly
// without a token, and /demo must keep serving the fixture scene.
//   BASE_URL=https://runko.victornazzaro.com node scripts/public-check.mjs
import { mkdirSync } from "node:fs";
import { chromium } from "playwright-core";

const base = process.env.BASE_URL ?? "https://runko.victornazzaro.com";
const out = new URL("../screenshots/public", import.meta.url).pathname;
mkdirSync(out, { recursive: true });

const browser = await chromium.launch();
const context = await browser.newContext({ viewport: { width: 1440, height: 950 } });
const page = await context.newPage();
function fail(msg) {
  console.error("FAIL:", msg);
  process.exitCode = 1;
}

// Root, no token: live transport, unauthenticated errors - never demo data.
await page.goto(`${base}/changes`);
await page.waitForTimeout(1500);
const root = await page.textContent("body");
if (!root.includes("Live, no token")) fail("expected the tokenless live badge");
if (!root.includes("Set token")) fail("expected the Set token button");
if (root.includes("storefront: surface SKU errors")) fail("root is showing DEMO fixture data");
await page.screenshot({ path: `${out}/root-tokenless.png` });

// The RPC actually went same-origin to runkod and came back 401.
const rpc = await page.evaluate(async (b) => {
  const r = await fetch(`${b}/runko.v1.ChangeService/ListChanges`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: "{}",
  });
  return r.status;
}, base);
if (rpc !== 401) fail(`same-origin RPC without token: want 401, got ${rpc}`);

// /demo: fixture scene, fully anonymous, no live bleed.
await page.goto(`${base}/demo/changes`);
await page.waitForTimeout(1500);
const demo = await page.textContent("body");
if (!demo.includes("Demo data")) fail("demo badge missing under /demo");
if (!demo.includes("storefront: surface SKU errors")) fail("demo fixture scene missing");
await page.screenshot({ path: `${out}/demo.png` });

await browser.close();
console.log(process.exitCode ? "PUBLIC CHECK: FAILED" : "PUBLIC CHECK: OK");
