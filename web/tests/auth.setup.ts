import { expect, test as setup } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';

/**
 * 登录一次，把会话存成 storageState，后续用例直接复用。
 *
 * 走真实登录表单而不是直接调 /auth/login 塞 cookie：登录页本身也是要冒烟的对象，
 * 而且「表单提交后能不能落到模板库」正是最容易在改版里坏掉的一环。
 */
const STATE = path.resolve('.auth/state.json');

setup('登录并保存会话', async ({ page }) => {
  const username = process.env.E2E_USERNAME;
  const password = process.env.E2E_PASSWORD;
  setup.skip(
    !username || !password,
    '未提供 E2E_USERNAME / E2E_PASSWORD。E2E 需要真实账号，用环境变量传入，不要写进仓库。',
  );

  await page.goto('/login');
  // 登录页是左右分栏：左边品牌墙，右边表单。等分栏容器而不是等某个字，
  // 品牌区文案改过一次（「铸版」从 h1 挪到了标题旁边的 span）。
  await expect(page.locator('.login-split')).toBeVisible();
  await expect(page.getByRole('heading', { name: '登录' })).toBeVisible();

  await page.getByPlaceholder(/账号|用户名/).fill(username!);
  await page.locator('input[type="password"]').fill(password!);
  // exact: 必须写。登录页还有一枚「使用 APSSO 统一登录」，模糊匹配会命中两个按钮。
  await page.getByRole('button', { name: '登录', exact: true }).click();

  // 登录成功后前端会跳到模板库（或 redirect 指定的页）
  await page.waitForURL((u) => !u.pathname.startsWith('/login'), { timeout: 20_000 });
  await expect(page.locator('.df-page-title, .grid-card').first()).toBeVisible();

  fs.mkdirSync(path.dirname(STATE), { recursive: true });
  await page.context().storageState({ path: STATE });
});
