import { chromium } from '@playwright/test';
import fs from 'node:fs';

const OUT = process.env.SHOT_DIR;
const KEY = 'e2e-test-key';
const VIEWS = ['accounts', 'usage', 'packages', 'taskscenter', 'models', 'keys', 'config', 'logs', 'opsagent'];

fs.mkdirSync(OUT, { recursive: true });

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, colorScheme: 'light' });

await page.goto('http://127.0.0.1:7864/panel/');
await page.fill('#keyInput', KEY);
await page.click('#btnKey');
await page.waitForFunction(() => document.querySelector('#keyVeil')?.style.display === 'none' ||
  getComputedStyle(document.querySelector('#keyVeil')).display === 'none', null, { timeout: 10000 });

for (const v of VIEWS) {
  const link = page.locator(`.nav a[data-view="${v}"]`);
  if (await link.count() === 0) { console.log(`skip ${v}: no nav`); continue; }
  await link.click();
  await page.waitForTimeout(900);
  await page.screenshot({ path: `${OUT}/${v}.png`, fullPage: false });
  const metrics = await page.evaluate(() => {
    const ttl = document.querySelector('#ttl')?.textContent?.trim();
    const sec = document.querySelector('.nav')?.getBoundingClientRect();
    return { ttl, navWidth: sec ? Math.round(sec.width) : null };
  });
  console.log(v, JSON.stringify(metrics));
}

// 登录前的密钥门也留一张
await page.evaluate(() => localStorage.clear());
await page.goto('http://127.0.0.1:7864/panel/');
await page.waitForTimeout(600);
await page.screenshot({ path: `${OUT}/gate.png` });

await browser.close();
console.log('done ->', OUT);
