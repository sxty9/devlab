const { chromium } = await import('/home/nanu/devlab/node_modules/playwright/index.mjs');
const OUT = '/home/nanu/devlab/brand';
import { mkdirSync } from 'node:fs';
mkdirSync(OUT, { recursive: true });

// A DevLab OAuth-app logo, faithful to the in-app brand: the code-brackets glyph (icons.tsx CodeIcon,
// path "M9 8l-4 4 4 4M15 8l4 4-4 4") in Apple-blue accent (rgb 10 132 255) on a dark rounded tile
// with the same soft accent glow the Brand lockup uses. Full-bleed so GitHub's own rounding is clean.
const page = (size) => `
<!doctype html><html><head><meta charset="utf-8"><style>
  html,body{margin:0;padding:0}
  .wrap{width:${size}px;height:${size}px;display:flex;align-items:center;justify-content:center;
    background:radial-gradient(120% 120% at 30% 20%, #2c2c2e 0%, #1c1c1e 55%, #141416 100%);
    border-radius:${Math.round(size * 0.22)}px;position:relative;overflow:hidden}
  .glow{position:absolute;width:70%;height:70%;border-radius:50%;
    background:rgb(10 132 255);filter:blur(${Math.round(size * 0.14)}px);opacity:0.28}
  svg{position:relative;width:56%;height:56%}
  path{stroke:rgb(10 132 255);stroke-width:2.6;stroke-linecap:round;stroke-linejoin:round;fill:none}
</style></head><body>
  <div class="wrap">
    <div class="glow"></div>
    <svg viewBox="0 0 24 24"><path d="M9 8l-4 4 4 4M15 8l4 4-4 4"/></svg>
  </div>
</body></html>`;

const browser = await chromium.launch();
for (const size of [512, 256, 128]) {
  const p = await browser.newPage({ viewport: { width: size, height: size }, deviceScaleFactor: 1 });
  await p.setContent(page(size), { waitUntil: 'load' });
  await p.locator('.wrap').screenshot({ path: `${OUT}/devlab-logo-${size}.png`, omitBackground: true });
  await p.close();
  console.log(`  wrote devlab-logo-${size}.png`);
}
await browser.close();
