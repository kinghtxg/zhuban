import { expect, test, type Page } from '@playwright/test';

/**
 * 布局回归（「不要大面积空白 + 全站可拖动」这条需求的自证）
 *
 * 为什么这几条值得进 CI：
 *   这类问题**接口全绿、页面照样打得开**，只是看起来「空得离谱」或「拖不动」。
 *   curl 和接口测试永远发现不了，只能靠真实浏览器量几何。
 *
 * 断言原则：量数字，不看截图。截图和肉眼都会骗人（缩放、异步渲染、字体差异），
 * 而「卡片底边到视口底的像素差」是二值事实。
 */

/** 量一组几何信息，免得每个用例里重复写 evaluate */
async function geometry(page: Page) {
  return page.evaluate(() => {
    const rect = (sel: string) => {
      const el = document.querySelector(sel);
      if (!el) return null;
      const b = el.getBoundingClientRect();
      return { w: Math.round(b.width), h: Math.round(b.height), top: Math.round(b.top), bottom: Math.round(b.bottom) };
    };
    const scroll = document.querySelector('.app-scroll');
    const inner = document.querySelector('.data-card .arco-table-content-inner');
    return {
      viewport: { w: window.innerWidth, h: window.innerHeight },
      docScrollable: document.documentElement.scrollHeight - window.innerHeight,
      appScroll: scroll
        ? { h: Math.round(scroll.getBoundingClientRect().height), overflowY: getComputedStyle(scroll).overflowY }
        : null,
      card: rect('.data-card'),
      inner: inner ? { overflowY: getComputedStyle(inner).overflowY, clientH: inner.clientHeight } : null,
      sider: rect('.app-sider'),
    };
  });
}

/**
 * 等表格加载完。
 * ⚠️ 必须等：Arco 的 Spin 在 loading 时给 `.arco-spin-children::after` 加了
 *    `pointer-events: auto` 的全屏遮罩，这期间任何 mousedown 都打在遮罩上。
 *    不等这一下，拖动用例会偶发「拖了没反应」，看起来像功能坏了，其实只是快了半拍。
 */
async function waitTableReady(page: Page) {
  await page.waitForFunction(() => !document.querySelector('.arco-spin-loading'), null, { timeout: 20_000 });
}

test('外壳定高：整页不滚，滚动只发生在内容区', async ({ page }) => {
  await page.goto('/admin/users');
  await page.waitForSelector('.data-card .arco-table-th');

  const g = await geometry(page);

  // 这是「大段死白」的根因：外壳一旦是 min-height:100vh，内容就把 <html> 撑高，
  // 内容区自己的 overflow:auto 就永远拿不到滚动权，卡片也就没法「吃满剩余高度」。
  expect(g.docScrollable, '根文档不该出现滚动条').toBeLessThanOrEqual(1);
  expect(g.appScroll!.overflowY).toBe('auto');

  // 内容区高度 = 视口 - 顶栏 - 面包屑，必须是正的、且贴合
  expect(g.appScroll!.h).toBeGreaterThan(g.viewport.h * 0.7);
});

test('数据卡片吃满剩余高度，不留大片死白', async ({ page }) => {
  await page.goto('/admin/users');
  await page.waitForSelector('.data-card .arco-table-th');
  await waitTableReady(page);
  await page.waitForTimeout(300);

  const g = await geometry(page);
  // 卡片底边到视口底只该剩页面内边距（--space-6 = 24）。
  // 之前这里是 400+，也就是截图里那一片白。
  const gap = g.viewport.h - g.card!.bottom;
  expect(gap, `卡片下方还剩 ${gap}px 空白`).toBeLessThanOrEqual(32);
  expect(g.card!.h, '卡片高度应该接近可用高度').toBeGreaterThan(g.appScroll!.h * 0.7);
});

test('表体自己滚动，表头吸顶', async ({ page }) => {
  await page.goto('/admin/users');
  await page.waitForSelector('.data-card .arco-table-th');
  await waitTableReady(page);

  const g = await geometry(page);
  // 表体必须是滚动容器：否则纵向滚动只能交给外层，表头会跟着滚走
  expect(g.inner!.overflowY).toBe('auto');

  // 人为把表体压矮制造纵向溢出，再看表头是否仍贴在容器顶边。
  // 依赖真实行数的话，数据少的实例上这条永远测不到东西。
  const stuck = await page.evaluate(() => {
    const box = document.querySelector('.data-card .arco-table-content-inner') as HTMLElement;
    box.style.height = '70px';
    box.scrollTop = 30;
    const th = document.querySelector('.data-card .arco-table-th') as HTMLElement;
    return Math.abs(th.getBoundingClientRect().top - box.getBoundingClientRect().top);
  });
  expect(stuck, '表头没有吸顶，滚动后跟着内容跑了').toBeLessThan(2);
});

test('侧栏可拖：跟手、夹取、记忆、双击复位', async ({ page }) => {
  await page.goto('/admin/users');
  await page.waitForSelector('.app-sider-grip');
  await waitTableReady(page);

  const width = () => page.evaluate(() => Math.round(document.querySelector('.app-sider')!.getBoundingClientRect().width));
  const drag = async (dx: number) => {
    const b = (await page.locator('.app-sider-grip').boundingBox())!;
    await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2);
    await page.mouse.down();
    await page.mouse.move(b.x + b.width / 2 + dx, b.y + b.height / 2, { steps: 8 });
    await page.mouse.up();
    await page.waitForTimeout(200);
  };

  const start = await width();
  await drag(80);
  // 1:1 跟手：拖多少就宽多少（Arco 自带 ResizeBox 会因为 padding 记账每拖一次偏 6~7px）
  expect(await width(), '拖 80px 后宽度不对').toBe(start + 80);

  // 越界必须夹住，否则能把侧栏拖成 0 或占满整屏
  await drag(-400);
  expect(await width()).toBe(180);
  await drag(600);
  expect(await width()).toBe(420);

  // 刷新后仍在：列宽/侧栏宽是用户的一次性取舍，刷新就丢等于白拖
  await page.reload();
  await page.waitForSelector('.app-sider-grip');
  await waitTableReady(page);
  expect(await width()).toBe(420);

  const b = (await page.locator('.app-sider-grip').boundingBox())!;
  await page.mouse.dblclick(b.x + b.width / 2, b.y + b.height / 2);
  expect(await width(), '双击应复位到默认 220').toBe(220);
});

test('列宽可拖：每列都有手柄、拖动跟手、刷新后记住', async ({ page }) => {
  await page.goto('/admin/users');
  await page.waitForSelector('.data-card .df-col-resizer');
  await waitTableReady(page);

  const firstThWidth = () =>
    page.evaluate(() =>
      Math.round((document.querySelector('.data-card .df-col-resizer') as HTMLElement).closest('th')!.getBoundingClientRect().width),
    );

  // 手柄数量 = 列数：少一个就说明某列没接上 onHeaderCell
  const cols = await page.locator('.data-card .arco-table-th').count();
  const grips = await page.locator('.data-card .df-col-resizer').count();
  expect(grips).toBe(cols);

  const before = await firstThWidth();
  const b = (await page.locator('.data-card .df-col-resizer').first().boundingBox())!;
  await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2);
  await page.mouse.down();
  await page.mouse.move(b.x + b.width / 2 + 60, b.y + b.height / 2, { steps: 8 });
  await page.mouse.up();
  await page.waitForTimeout(300);

  const after = await firstThWidth();
  expect(after, `首列 ${before} → ${after}，没跟手`).toBe(before + 60);

  const stored = await page.evaluate(() => localStorage.getItem('df.cols.admin-users'));
  expect(stored, '列宽没有落 localStorage').toBeTruthy();

  await page.reload();
  await page.waitForSelector('.data-card .df-col-resizer');
  await waitTableReady(page);
  expect(await firstThWidth(), '刷新后列宽丢了').toBe(after);
});
