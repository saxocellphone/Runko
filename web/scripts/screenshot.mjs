// Headless visual smoke test: screenshots the main surfaces in both themes.
//
//   npx playwright-core install chromium-headless-shell   (once)
//   npm run dev                                            (another shell)
//   npm run screenshot
//
// Writes PNGs into screenshots/ (gitignored). BASE_URL overrides the dev
// server address.
import { mkdirSync } from "node:fs";
import { chromium } from "playwright-core";

const base = process.env.BASE_URL ?? "http://localhost:5173";
const out = new URL("../screenshots", import.meta.url).pathname;
mkdirSync(out, { recursive: true });

const browser = await chromium.launch();
const context = await browser.newContext({
  viewport: { width: 1440, height: 950 },
  deviceScaleFactor: 1.5,
});
const page = await context.newPage();
page.on("pageerror", (e) => console.error("page error:", e.message));

async function shot(path, theme, name, prepare) {
  await page.goto(`${base}${path}`);
  await page.evaluate((t) => localStorage.setItem("runko-theme", t), theme);
  await page.goto(`${base}${path}`);
  await page.waitForTimeout(800);
  if (prepare) await prepare(page);
  await page.screenshot({ path: `${out}/${name}.png` });
  console.log(`${name}.png`);
}

for (const theme of ["dark", "light"]) {
  await shot("/changes", theme, `changes-${theme}`);
  await shot("/changes", theme, `change-${theme}`, async (p) => {
    await p.click(".change-title-link");
    await p.waitForTimeout(900);
  });
}
await shot("/projects", "dark", "projects-dark");
await shot("/workspaces", "dark", "workspaces-dark");
await shot("/search", "dark", "search-dark", async (p) => {
  await p.fill('input[type="text"]', "invalid_sku");
  await p.click('button[type="submit"]');
  await p.waitForTimeout(600);
});

await browser.close();
