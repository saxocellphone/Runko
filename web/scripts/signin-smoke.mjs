// signin-smoke: the sign-in page's smoke + edge matrix, driven over the
// REAL stack (compiled runkod hub with signup/org-create on, Vite dev
// server with NO baked token, headless Chromium). Every scenario runs in
// a fresh browser context - a clean localStorage, like a first visit.
//
// Smoke: the gate renders; sign-up (create + join) lands signed in
// inside the org; sign-out/sign-in round-trips; the operator signs in
// with the deploy token like anyone else - and sees NO org drop-down
// (removed 2026-07-17: orgs are /<org> URLs, operators included).
// Edge: the three refusal mappings (401 wrong password / 403 wrong org /
// 404 no such org) render their human messages; the org field scopes to
// the URL's org; the prod-observed cross-org rebind (same name+password
// in two orgs, sign in to one from under the other's URL) lands under
// the AUTHENTICATED org's URL; the signup length gate and name_taken.
//
// Run locally (needs a compiled daemon + playwright-core's chromium):
//   go build -o /tmp/runkod-signin ./runkod/cmd/runkod
//   cd web && RUNKOD_BIN=/tmp/runkod-signin node scripts/signin-smoke.mjs
// Not in CI (compiled-daemon dependency, like stack-smoke.mjs); the
// server-side truths are pinned by runkod's signin_smoke_test.go and the
// client org-binding rule by src/lib/orgsession.test.ts.
import { spawn, execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { chromium } from "playwright-core";

const bin = process.env.RUNKOD_BIN ?? "/tmp/runkod-signin";
const scratch = mkdtempSync(join(tmpdir(), "signin-smoke-"));
const out = new URL("../screenshots/signin", import.meta.url).pathname;
mkdirSync(out, { recursive: true });

const PW = "one-pw-everywhere"; // same combo in BOTH orgs - the prod edge
const OPS_TOKEN = "ops-tok";

function waitLine(proc, pattern, timeoutMs = 30000) {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error(`timeout waiting for ${pattern}`)), timeoutMs);
    let buf = "";
    const f = (d) => {
      buf += d.toString();
      const m = buf.match(pattern);
      if (m) {
        clearTimeout(t);
        resolve(m);
      }
    };
    proc.stdout.on("data", f);
    proc.stderr.on("data", f);
  });
}

// --- the stack: bare default-org repo, daemon, vite ---
execFileSync("git", ["init", "--bare", "-b", "main", join(scratch, "mono.git")]);
const daemon = spawn(bin, [
  "serve",
  "--repo-dir", join(scratch, "mono.git"),
  "--orgs-dir", join(scratch, "orgs"),
  "--addr", "127.0.0.1:0",
  "--trunk", "main",
  "--token", OPS_TOKEN,
  "--allow-signup",
  "--allow-org-create",
  "--insecure-skip-secret-scan",
]);
const [, U] = await waitLine(daemon, /at (http:\/\/127\.0\.0\.1:\d+)/);
console.log("runkod:", U, " scratch:", scratch);

// detached => own process group, so the cleanup kill(-pid) below reaps
// vite's real child too (killing just the npx wrapper strands it, and a
// stranded vite makes the next --strictPort run time out).
const vite = spawn("npx", ["vite", "--port", "5193", "--strictPort"], {
  env: { ...process.env, VITE_RUNKO_URL: U, VITE_RUNKO_TOKEN: "" },
  detached: true,
});
await waitLine(vite, /localhost:5193/);
const base = "http://localhost:5193";

const browser = await chromium.launch();
let failed = false;
const fail = (m) => {
  console.error("FAIL:", m);
  failed = true;
};
const ok = (m) => console.log("ok:", m);

const pageErrors = [];
async function freshPage() {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 900 } });
  const page = await ctx.newPage();
  page.on("pageerror", (e) => pageErrors.push(e.message));
  return page;
}

// The login form (LoginPage.tsx): label-wrapped inputs, .login-error,
// .login-submit, .login-link mode switches.
const orgInput = 'label.login-label:has-text("Organization") input';
const nameInput = 'label.login-label:has-text("Name") input';
const passInput = 'label.login-label:has-text("Password") input';

async function waitForLogin(page) {
  await page.waitForSelector(".login-card", { timeout: 15000 });
}

async function submitAndSettle(page, ms = 2500) {
  await page.click(".login-submit");
  await page.waitForTimeout(ms);
}

async function signInVia(page, org, name, pass) {
  await waitForLogin(page);
  await page.fill(orgInput, org);
  await page.fill(nameInput, name);
  await page.fill(passInput, pass);
  await submitAndSettle(page);
}

async function signUpVia(page, mode, org, name, pass) {
  await waitForLogin(page);
  await page.click('.login-link:text-is("Create an account")');
  await page.click(`.login-orgmode label:has-text("${mode === "create" ? "Create a new org" : "Join an existing org"}")`);
  await page.fill(orgInput, org);
  await page.fill(nameInput, name);
  await page.fill(passInput, pass);
  await submitAndSettle(page, 3500);
}

const signedInBody = async (page) => (await page.textContent("body")) ?? "";

// ---- S1: the gate renders with the full form and a disabled submit.
{
  const page = await freshPage();
  await page.goto(`${base}/changes`);
  await waitForLogin(page);
  for (const sel of [orgInput, nameInput, passInput]) {
    if (!(await page.$(sel))) fail(`S1: missing field ${sel}`);
  }
  if (!(await page.$(".login-submit[disabled]"))) fail("S1: submit not disabled on the empty form");
  await page.screenshot({ path: `${out}/s1-gate.png` });
  ok("S1 gate renders");
  await page.context().close();
}

// ---- S2: sign-up creating org-x lands signed in as casey.
{
  const page = await freshPage();
  await page.goto(`${base}/changes`);
  await signUpVia(page, "create", "org-x", "casey", PW);
  const body = await signedInBody(page);
  if (!body.includes("casey")) fail("S2: signed-in identity missing after signup-create");
  if (await page.$(".login-card")) fail("S2: still on the login gate after signup");
  await page.screenshot({ path: `${out}/s2-signup-create.png` });
  ok("S2 signup-create lands inside org-x");
  await page.context().close();
}

// ---- S3: sign out, sign back in (the steady path).
{
  const page = await freshPage();
  await page.goto(`${base}/changes`);
  await signInVia(page, "org-x", "casey", PW);
  if (await page.$(".login-card")) fail("S3: sign-in did not leave the gate");
  await page.click('button:text-is("Sign out")');
  await page.waitForTimeout(1500);
  await page.goto(`${base}/changes`);
  await waitForLogin(page); // signed out -> gate again
  ok("S3 sign-in and sign-out round-trip");
  await page.context().close();
}

// ---- S4: riley founds org-y (second org, second account).
{
  const page = await freshPage();
  await page.goto(`${base}/changes`);
  await signUpVia(page, "create", "org-y", "riley", "rileys-own-pw");
  if (await page.$(".login-card")) fail("S4: riley's signup did not land");
  ok("S4 riley founds org-y");
  await page.context().close();
}

// ---- S5: casey JOINS org-y under the SAME name+password combo.
{
  const page = await freshPage();
  await page.goto(`${base}/changes`);
  await signUpVia(page, "join", "org-y", "casey", PW);
  if (await page.$(".login-card")) fail("S5: same-combo join did not land");
  const body = await signedInBody(page);
  if (!body.includes("casey")) fail("S5: casey identity missing after join");
  await page.screenshot({ path: `${out}/s5-same-combo-join.png` });
  ok("S5 casey joins org-y with the same combo");
  await page.context().close();
}

// ---- E1/E2/E3: the three refusal mappings render as human messages.
{
  const page = await freshPage();
  await page.goto(`${base}/changes`);
  await signInVia(page, "org-x", "casey", "wrong-password!");
  let err = (await page.textContent(".login-error")) ?? "";
  if (!err.includes("wrong name or password")) fail(`E1: 401 mapping, got "${err}"`);
  else ok("E1 wrong password says so");

  await page.fill(orgInput, "org-x");
  await page.fill(nameInput, "riley");
  await page.fill(passInput, "rileys-own-pw");
  await submitAndSettle(page);
  err = (await page.textContent(".login-error")) ?? "";
  if (!err.includes("not a member")) fail(`E2: 403 mapping, got "${err}"`);
  else ok("E2 valid account, wrong org says so");

  await page.fill(orgInput, "org-nope");
  await page.fill(nameInput, "casey");
  await page.fill(passInput, PW);
  await submitAndSettle(page);
  err = (await page.textContent(".login-error")) ?? "";
  if (!err.includes("no org named")) fail(`E3: 404 mapping, got "${err}"`);
  else ok("E3 unknown org says so");
  await page.screenshot({ path: `${out}/e3-refusals.png` });
  await page.context().close();
}

// ---- E4+E5: the prod repro. Under org-y's own URL the form scopes to
// org-y; signing in AS org-x must land under /org-x as org-x's casey -
// a bare reload used to stay on /org-y, where the same combo verifies
// against org-y's DIFFERENT account.
{
  const page = await freshPage();
  await page.goto(`${base}/org-y/changes`);
  await waitForLogin(page);
  const prefill = await page.inputValue(orgInput);
  if (prefill !== "org-y") fail(`E5: org field must scope to the URL's org, got "${prefill}"`);
  else ok("E5 org field prefilled from the URL");
  await page.fill(orgInput, "org-x");
  await page.fill(nameInput, "casey");
  await page.fill(passInput, PW);
  await submitAndSettle(page, 3500);
  const url = page.url();
  if (!new URL(url).pathname.startsWith("/org-x")) {
    fail(`E4: signed in to org-x but landed at ${url} - the cross-org rebind is back`);
  } else ok("E4 sign-in lands under the org that authenticated");
  if (await page.$(".login-card")) fail("E4: still on the gate");
  await page.screenshot({ path: `${out}/e4-cross-org-landing.png` });
  await page.context().close();
}

// ---- E6: the operator signs in like anyone else (deploy token as the
// password, any name) - and gets NO org drop-down even though the org
// listing answers operators with the whole estate.
{
  const page = await freshPage();
  await page.goto(`${base}/changes`);
  await signInVia(page, "mono", "op", OPS_TOKEN);
  if (await page.$(".login-card")) fail("E6: operator sign-in did not land");
  const selects = await page.$$('header select, select.org-select, select[aria-label="Organization"]');
  if (selects.length !== 0) fail(`E6: an org drop-down still renders for the operator (${selects.length} selects in the header)`);
  else ok("E6 no org drop-down for the operator");
  await page.screenshot({ path: `${out}/e6-operator-no-dropdown.png` });
  await page.context().close();
}

// ---- E7: the signup length gate (min 8) keeps submit disabled.
{
  const page = await freshPage();
  await page.goto(`${base}/changes`);
  await waitForLogin(page);
  await page.click('.login-link:text-is("Create an account")');
  await page.fill(orgInput, "org-q");
  await page.fill(nameInput, "quinn");
  await page.fill(passInput, "short");
  if (!(await page.$(".login-submit[disabled]"))) fail("E7: sub-8-char signup password not gated");
  else ok("E7 short signup password stays gated");
  await page.context().close();
}

// ---- E8: name_taken surfaces the server's structured message.
{
  const page = await freshPage();
  await page.goto(`${base}/changes`);
  await signUpVia(page, "join", "org-x", "casey", "a-different-pw-123");
  const err = (await page.textContent(".login-error")) ?? "";
  // The server's name_taken message reads "the name … is already in use".
  if (!/already in use/i.test(err)) fail(`E8: name_taken mapping, got "${err}"`);
  else ok("E8 name_taken renders the structured message");
  await page.screenshot({ path: `${out}/e8-name-taken.png` });
  await page.context().close();
}

if (pageErrors.length) fail(`page JS errors: ${pageErrors.join(" | ")}`);

await browser.close();
try {
  process.kill(-vite.pid, "SIGTERM");
} catch {
  vite.kill();
}
daemon.kill();
if (failed) {
  console.error("signin-smoke: FAILURES (screenshots in web/screenshots/signin)");
  process.exit(1);
}
console.log("signin-smoke: all scenarios green (screenshots in web/screenshots/signin)");
