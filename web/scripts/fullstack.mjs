// Full-stack drive: real runkod (Connect handlers, runkod/rpc.go) <-
// Connect-Web <- React UI in headless Chromium. Asserts the live data
// renders, clicks Approve and Land for real, and confirms /demo stays on
// the fake transport. Expects:
//
//   runkod serve --repo-dir ... --token dev --insecure-skip-secret-scan
//     seeded with: one landed change, one OPEN change titled
//     "pricing: add pricing-lib..." (check green, owner approval
//     outstanding), and a workspace "alice-fix"
//   VITE_RUNKO_URL=http://127.0.0.1:<port> VITE_RUNKO_TOKEN=dev npm run dev
//
// then: BASE_URL=http://localhost:5173 node scripts/fullstack.mjs
import { mkdirSync } from "node:fs";
import { chromium } from "playwright-core";

const base = process.env.BASE_URL ?? "http://localhost:5173";
const out = new URL("../screenshots/fullstack", import.meta.url).pathname;
mkdirSync(out, { recursive: true });

const browser = await chromium.launch();
const context = await browser.newContext({ viewport: { width: 1440, height: 950 } });
const page = await context.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));

async function shot(name) {
  await page.screenshot({ path: `${out}/${name}.png` });
  console.log("shot:", name);
}
function fail(msg) {
  console.error("FAIL:", msg);
  process.exitCode = 1;
}

// 1. Inbox shows the real open change from runkod.
await page.goto(`${base}/changes`);
await page.waitForTimeout(1200);
const inbox = await page.textContent("body");
if (!inbox.includes("pricing: add pricing-lib")) fail("open change missing from inbox");
if (inbox.includes("Demo data")) fail("root app still shows the demo badge");
if (!inbox.includes("Live")) fail("live badge missing");
await shot("01-inbox-live");

// 2. Change page: diff + gates from the real daemon.
await page.click(".change-title-link");
await page.waitForTimeout(1500);
const changeBody = await page.textContent("body");
if (!changeBody.includes("group:commerce-eng")) fail("owner gate missing");
if (!changeBody.includes("unit")) fail("check gate missing");
if (!changeBody.includes("pricing.go")) fail("diff missing pricing.go");
await shot("02-change-live");

// 3. Approve as user:reviewer (author is anonymous; any non-author name).
await page.fill('input[aria-label="approve as"]', "user:reviewer");
await page.click('button.btn:text-is("Approve")');
await page.waitForTimeout(1500);
const approved = await page.textContent("body");
if (!approved.includes("Ready to land")) fail("approve did not unlock the land gate");
await shot("03-approved");

// 4. Land through the UI.
await page.click('button.btn-primary:text-is("Land")');
await page.waitForTimeout(2000);
const landed = await page.textContent("body");
if (!landed.includes("Landed as")) fail("land banner missing");
await shot("04-landed");

// 5. Projects / browse / workspaces pages read the new trunk.
await page.goto(`${base}/projects`);
await page.waitForTimeout(1200);
const projects = await page.textContent("body");
if (!projects.includes("pricing-lib")) fail("projects page missing pricing-lib post-land");
await shot("05-projects");

await page.goto(`${base}/browse/libs/pricing`);
await page.waitForTimeout(1200);
const browse = await page.textContent("body");
if (!browse.includes("pricing.go")) fail("browse page missing pricing.go");
await shot("06-browse");

await page.goto(`${base}/workspaces`);
await page.waitForTimeout(1200);
const ws = await page.textContent("body");
if (!ws.includes("alice-fix")) fail("workspaces page missing alice-fix");
await shot("07-workspaces");

// 6. The demo mount still serves the fixture scene, untouched.
await page.goto(`${base}/demo/changes`);
await page.waitForTimeout(1200);
const demo = await page.textContent("body");
if (!demo.includes("Demo data")) fail("demo badge missing under /demo");
if (demo.includes("pricing: add pricing-lib")) fail("demo shows LIVE data - transport bleed!");
await shot("08-demo");

if (errors.length) fail("page errors: " + errors.join(" | "));
await browser.close();
console.log(process.exitCode ? "FULL-STACK: FAILED" : "FULL-STACK: OK");
