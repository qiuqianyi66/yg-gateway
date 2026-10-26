// V3-6 面板冒烟：密钥门 → 登录 → 八视图切换 → opsagent 卡片。
// 用户可见行为断言 ≥10 条（本文件共 18 条）；全程收集 pageerror / console.error，
// 任一出现即失败（"无 JS 报错"是八视图可用性的硬条件）。
import { test, expect, Page } from '@playwright/test';

const KEY = 'e2e-test-key'; // 与 scripts/e2e-server.ps1 的 WB2A_API_KEY 一致

const TITLES: Record<string, string> = {
  accounts: '账号池',
  usage: '用量',
  packages: '积分构成',
  taskscenter: '任务中心',
  models: '模型与档位',
  keys: 'API Key 管理',
  config: '配置',
  logs: '运行日志',
};
const VIEWS = Object.keys(TITLES); // 八个视图

function watchErrors(page: Page) {
  const errs: string[] = [];
  page.on('pageerror', e => errs.push('pageerror: ' + e.message));
  page.on('console', m => {
    if (m.type() !== 'error') return;
    const t = m.text();
    // 浏览器为失败 HTTP 请求自动打的资源日志（401/503 等）不是 JS 报错：
    // 401 是密钥门必经路径（未登录探测），503 是空池下 models 的已知降级。
    // 真正的 JS 问题（pageerror / CSP 违规 / 语法错误）不过滤。
    if (t.startsWith('Failed to load resource')) return;
    errs.push('console: ' + t);
  });
  return errs;
}

async function login(page: Page) {
  await expect(page.locator('#keyVeil')).toBeVisible();
  await page.fill('#keyInput', KEY);
  await page.click('#btnKey');
  await expect(page.locator('#keyVeil')).toBeHidden();
}

test('密钥门：未登录自动弹出，错误密钥被拒不放行', async ({ page }) => {
  const errs = watchErrors(page);
  await page.goto('/panel/');
  // 1. 未带密钥 → 门自动弹出（checkAuthGate 401 触发 openKey）
  await expect(page.locator('#keyVeil')).toBeVisible();
  // 2. 填错误密钥 → 错误提示可见
  await page.fill('#keyInput', 'wrong-key');
  await page.click('#btnKey');
  await expect(page.locator('#keyErr')).toBeVisible();
  // 3. 门仍未放行
  await expect(page.locator('#keyVeil')).toBeVisible();
  // 4. 全程无 JS 报错
  expect(errs).toEqual([]);
});

test('正确密钥进入账号池并渲染统计条', async ({ page }) => {
  const errs = watchErrors(page);
  await page.goto('/panel/');
  await login(page);
  // 5. 标题切到账号池
  await expect(page.locator('#ttl')).toHaveText('账号池');
  // 6. 统计条已出数据（不再是占位 "-"）
  await expect(page.locator('#sHealthy')).not.toHaveText('-');
  expect(errs).toEqual([]);
});

test('八个视图全部可切换，标题/可见性/hash 正确，全程无 JS 报错', async ({ page }) => {
  const errs = watchErrors(page);
  await page.goto('/panel/');
  await login(page);
  for (const v of VIEWS) {
    await page.click(`.nav a[data-view="${v}"]`);
    // 7-14. 每个视图 section 可见
    await expect(page.locator('#view-' + v)).toBeVisible();
    // 15-22. 标题正确
    await expect(page.locator('#ttl')).toHaveText(TITLES[v]);
    // 23-30. hash 已写入地址栏
    await expect(page).toHaveURL(new RegExp('#' + v + '$'));
  }
  // 31. 全程无 JS 报错
  expect(errs).toEqual([]);
});

test('opsagent 卡片存在且可加载（空池空态）', async ({ page }) => {
  const errs = watchErrors(page);
  await page.goto('/panel/');
  await login(page);
  // 32. opsagent 卡片在账号池视图可见
  await expect(page.locator('#opsagentBox')).toBeVisible();
  // 33. 点击「加载 / 刷新」→ 初始提示被替换（至少进入"加载中…"或空态）
  await page.click('#opsagentReload');
  await expect(page.locator('#opsagentBody')).not.toHaveText(
    /点击「加载 \/ 刷新」查看未知台账与最近审计轮/);
  // 34. 空池空态文案（零账号 → 暂无审计轮）
  await expect(page.locator('#opsagentBody')).toContainText(/暂无审计轮|未启用/);
  expect(errs).toEqual([]);
});
