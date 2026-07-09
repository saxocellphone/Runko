// stack-smoke: DOM-vs-API cross-check for stack visualization edge
// cases (2026-07-09; found live: abandoning a stack's bottom left the
// pending child rendered directly on main with a green mergeable chip).
// Drives a real daemon with real pushes; for every phase the page's
// rendering must match the server's own answers: mergeable chips ==
// merge-requirements, trunk anchors == base_on_trunk, stack count ==
// ancestry-derived forests.
//
// Run locally:  go build -o /tmp/runkod-smokedemo ./runkod/cmd/runkod
//               (adjust S below)  then: node scripts/stack-smoke.mjs
// Not in CI: it needs a compiled daemon; the same truths are pinned by
// runkod's TestMergeRequirementsStackedBaseBlockers + vitest fixtures.
import { spawn } from "node:child_process";
import { mkdirSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { chromium } from "playwright-core";
const S = "/tmp/claude-1000/-home-carbon-Documents-Runko/52850ae1-72ef-4647-9016-f66126a0b42d/scratchpad";
const out = `${S}/stacksmoke`;
mkdirSync(out, { recursive: true });
function waitLine(proc, pattern, timeoutMs = 20000) {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error("timeout")), timeoutMs);
    let buf = "";
    const f = (d) => { buf += d.toString(); const m = buf.match(pattern); if (m) { clearTimeout(t); resolve(m); } };
    proc.stdout.on("data", f); proc.stderr.on("data", f);
  });
}
const daemon = spawn(`${S}/runkod-smokedemo`, ["serve",
  "--repo-dir", `${S}/smokedemo/monorepo.git`, "--addr", "127.0.0.1:0", "--trunk", "main",
  "--token", "demo-tok", "--insecure-skip-secret-scan", "--allow-workspaceless-changes",
]);
const [, U] = await waitLine(daemon, /at (http:\/\/127\.0\.0\.1:\d+)/);
const auth = { Authorization: "Bearer demo-tok" };

// --- build state with real git ---
const work = `${S}/smokedemo/work`;
const git = (args, cwd = work) => execFileSync("git", args, { cwd, env: { ...process.env, GIT_AUTHOR_NAME: "t", GIT_AUTHOR_EMAIL: "t@t", GIT_COMMITTER_NAME: "t", GIT_COMMITTER_EMAIL: "t@t" } }).toString();
mkdirSync(work, { recursive: true });
execFileSync("git", ["clone", U.replace("http://", "http://x:demo-tok@") + "/monorepo.git", work]);
const write = (p, c) => execFileSync("bash", ["-c", `mkdir -p $(dirname ${work}/${p}) && printf '%b' "${c}" > ${work}/${p}`]);

// trunk bootstrap
write("svc/PROJECT.yaml", "schema: project/v1\\nname: svc\\ntype: service\\n");
git(["add", "-A"]); git(["commit", "-m", "bootstrap\n\nChange-Id: Iaaaa000000000000000000000000000000000000"]);
git(["push", "origin", "+HEAD:refs/for/main"]);
let r = await fetch(`${U}/api/changes/Iaaaa000000000000000000000000000000000000/land`, { method: "POST", headers: { ...auth, "Content-Type": "application/json" }, body: "{}" });
if (r.status !== 200) { console.error("bootstrap land", r.status, await r.text()); process.exit(1); }
git(["fetch", "origin"]); git(["reset", "--hard", "origin/main"]);

// single trunk-based change
write("svc/single.go", "package svc // single");
git(["add", "-A"]); git(["commit", "-m", "single: standalone\n\nChange-Id: I1111000000000000000000000000000000000000"]);
git(["push", "origin", "+HEAD:refs/for/main"]);
git(["reset", "--hard", "origin/main"]);
// stack A <- B
write("svc/a.go", "package svc // a");
git(["add", "-A"]); git(["commit", "-m", "stackA: bottom\n\nChange-Id: I2222000000000000000000000000000000000000"]);
write("svc/b.go", "package svc // b");
git(["add", "-A"]); git(["commit", "-m", "stackB: top\n\nChange-Id: I3333000000000000000000000000000000000000"]);
git(["push", "origin", "+HEAD:refs/for/main"]);

// --- server truth helpers ---
async function apiState() {
  const changes = await (await fetch(`${U}/api/changes?state=open`, { headers: auth })).json();
  const reqs = {};
  for (const c of changes) {
    reqs[c.ChangeKey] = await (await fetch(`${U}/api/changes/${c.ChangeKey}/merge-requirements`, { headers: auth })).json();
  }
  return { changes, reqs };
}

const vite = spawn("npx", ["vite", "--port", "5192", "--strictPort"], { env: { ...process.env, VITE_RUNKO_URL: U, VITE_RUNKO_TOKEN: "demo-tok" } });
await waitLine(vite, /localhost:5192/);
const base = "http://localhost:5192";
const browser = await chromium.launch();
const page = await (await browser.newContext({ viewport: { width: 1440, height: 1100 } })).newPage();
let failed = false;
const fail = (m) => { console.error("FAIL:", m); failed = true; };

// crossCheck: DOM must match server truth.
async function crossCheck(phase, wantCards, wantWarnAnchors) {
  await page.goto(`${base}/changes`);
  await page.waitForTimeout(2200);
  const api = await apiState();
  const cards = await page.$$eval("[class*='stack-card'], section.card", (els) => els.length);
  const cardHeads = await page.$$eval(".stack-head, .stack-card-head", (els) => els.map((e) => e.textContent));
  // mergeable chips: count green "mergeable" texts == API-mergeable count
  const domMergeable = await page.$$eval(".chip-green", (els) => els.filter((e) => e.textContent === "mergeable").length);
  const apiMergeable = Object.values(api.reqs).filter((r) => r.mergeable).length;
  if (domMergeable !== apiMergeable) fail(`${phase}: mergeable chips ${domMergeable} != API ${apiMergeable}`);
  const warnAnchors = await page.$$eval(".anchor-warn", (els) => els.length);
  if (warnAnchors !== wantWarnAnchors) fail(`${phase}: warn anchors ${warnAnchors} != expected ${wantWarnAnchors}`);
  const mainAnchors = await page.$$eval(".stack-row-trunk .change-line", (els) => els.filter((e) => e.textContent === "main").length);
  const totalRoots = mainAnchors + warnAnchors;
  if (totalRoots !== wantCards) fail(`${phase}: stack roots ${totalRoots} != expected ${wantCards}`);
  await page.screenshot({ path: `${out}/${phase}.png`, fullPage: true });
  console.log(`${phase}: cards=${totalRoots} warn=${warnAnchors} mergeable dom=${domMergeable} api=${apiMergeable}`);
  return api;
}

// Phase 1: single + stack(A<-B): 2 stacks, no warnings; A+single mergeable, B blocked on A.
let api = await crossCheck("phase1-two-stacks", 2, 0);
const bReq = api.reqs["I3333000000000000000000000000000000000000"];
if (bReq.mergeable !== false || !JSON.stringify(bReq.blockers).includes("I2222")) fail("B should be blocked naming A: " + JSON.stringify(bReq));

// Phase 2: abandon A -> B orphaned: still 2 stacks, ONE warn anchor, B blocked (abandoned).
r = await fetch(`${U}/api/changes/I2222000000000000000000000000000000000000/abandon`, { method: "POST", headers: { ...auth, "Content-Type": "application/json" }, body: "{}" });
if (r.status !== 200) fail("abandon A: " + r.status);
api = await crossCheck("phase2-orphaned", 2, 1);
const bReq2 = api.reqs["I3333000000000000000000000000000000000000"];
if (bReq2.mergeable !== false || !JSON.stringify(bReq2.blockers).includes("abandoned")) fail("orphaned B blocker wrong: " + JSON.stringify(bReq2.blockers));

// Phase 3: land the single change -> 1 stack (orphan), warn anchor persists.
r = await fetch(`${U}/api/changes/I1111000000000000000000000000000000000000/land`, { method: "POST", headers: { ...auth, "Content-Type": "application/json" }, body: "{}" });
if (r.status !== 200) fail("land single: " + r.status + " " + (await r.text()));
api = await crossCheck("phase3-after-land", 1, 1);

await browser.close();
daemon.kill(); vite.kill();
console.log(failed ? "RESULT: FAIL" : "RESULT: OK");
process.exit(failed ? 1 : 0);
