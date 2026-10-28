import { defineConfig, devices } from '@playwright/test';

/**
 * 前端 E2E 冒烟配置。
 *
 * 刻意只做「冒烟」：三条覆盖登录、模板库、编辑器相关的首屏，目的是在每次
 * 发版前把「页面根本打不开」「登录进不去」「列表空白」这类事故挡在部署之前。
 * 全流程用例（生成 → 下载 → 回调）留在后端 Go 测试与验收脚本里，那里更适合
 * 断言产物内容。
 *
 * 浏览器用本机已有的 Chrome（channel: 'chrome'），不下载 Playwright 自带的
 * Chromium：这个项目是单机内网部署，多下 150MB 浏览器没有收益。
 *
 * 凭据从环境变量读，不写进仓库：
 *   E2E_BASE_URL=http://127.0.0.1:18100 E2E_USERNAME=admin E2E_PASSWORD=*** npm run test:e2e
 */
const BASE_URL = process.env.E2E_BASE_URL || 'http://127.0.0.1:18100';

export default defineConfig({
  testDir: './tests',
  // 冒烟用例之间没有共享状态，但都打同一个后端：串行更省事，失败也更好读。
  workers: 1,
  retries: 0,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: [['list']],
  use: {
    baseURL: BASE_URL,
    // 自签证书的内网部署也要能跑
    ignoreHTTPSErrors: true,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'setup',
      testMatch: /auth\.setup\.ts/,
      use: { ...devices['Desktop Chrome'], channel: 'chrome' },
    },
    {
      name: 'smoke',
      testMatch: /.*\.spec\.ts/,
      dependencies: ['setup'],
      use: {
        ...devices['Desktop Chrome'],
        channel: 'chrome',
        storageState: '.auth/state.json',
      },
    },
  ],
});
