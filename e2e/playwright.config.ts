// V3-6 面板冒烟 E2E 配置。
// 测试实例：scripts/e2e-server.ps1 在 %TEMP%\yg-e2e 起一个隔离网关
// （端口 7864，api_key=e2e-test-key，零账号池 → 不触上游），与生产 7863 完全隔离。
import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  timeout: 30_000,
  expect: { timeout: 10_000 },
  // 面板共用同一测试实例且互有轮询：串行单 worker，避免互相干扰。
  fullyParallel: false,
  workers: 1,
  reporter: [['list']],
  use: {
    baseURL: 'http://127.0.0.1:7864',
    // 固定浅色：面板 theme=auto 跟随 prefers-color-scheme，不固定会随系统漂移
    // （panel-spec 真机验证踩过：两张"不同主题"截图字节相同）。
    colorScheme: 'light',
    trace: 'retain-on-failure',
  },
  webServer: {
    command: 'pwsh -NoProfile -File scripts/e2e-server.ps1',
    cwd: __dirname,
    url: 'http://127.0.0.1:7864/panel/',
    reuseExistingServer: false,
    timeout: 30_000,
    stdout: 'pipe',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
});
