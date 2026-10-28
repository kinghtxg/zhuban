import { expect, test } from '@playwright/test';

/**
 * 六条冒烟用例。
 *
 * 挑选标准：都是「改了别的地方、结果这里坏了」的高频点，且失败信息一眼能定位。
 *  1. 模板库能列出卡片，且缩略图位置有兜底图标（不是一片灰框）
 *  2. 版本页的上传区是真的拖拽区（虚线框 + 拖拽文案），不是一行裸文字
 *  3. 投递调试台未选模板时给的是说明块，不是一个 320px 的白框
 *  4. 在线编辑器能把 docx 解析成块并选中（这条链路最长：后端解析 + 前端布局）
 *  5. 新建印章填参数后能出实时预览（走 render 服务，跨了 Go → Python）
 *  6. 系统设置保存能落地并回执（表单 + 即时生效语义）
 *
 * 第 2、3 条对应的都是曾经上线过的真实缺陷，回归价值最高。
 * 第 4–6 条覆盖的是此前完全没有自动化、只能靠人点的交互。
 */

test('模板库：卡片渲染且缩略图有兜底', async ({ page }) => {
  await page.goto('/templates');

  // 至少有一个模板卡片（内置模板播种后必然存在）
  const cards = page.locator('.grid-card');
  await expect(cards.first()).toBeVisible();

  // 缩略图盒子必须存在；里面的兜底图标要么被真图盖住，要么露出来——
  // 无论哪种，盒子本身不该是「什么都没有」的空壳。
  const thumb = page.locator('.thumb-box').first();
  await expect(thumb).toBeVisible();
  await expect(thumb.locator('.thumb-fallback')).toHaveCount(1);

  // 页面不该停留在骨架/加载态
  await expect(page.locator('.df-page-title')).toContainText('模板');
});

test('版本历史：上传区是可拖拽的虚线区，不是裸文字', async ({ page }) => {
  await page.goto('/templates');
  await page.locator('.grid-card-title').first().click();
  await expect(page).toHaveURL(/\/templates\/tpl_/);

  // Arco 的 Tabs 头部不是 role=tab（它渲染成普通 div），只能按类名+文案定位。
  const versionsTab = page.locator('.arco-tabs-header-title').filter({ hasText: '版本历史' });
  await expect(versionsTab).toBeVisible();
  await versionsTab.click();
  await expect(page).toHaveURL(/\/versions$/);

  // Arco 的默认拖拽区类名。它只在「Upload 没有 children」时才会被渲染，
  // 一旦有人把说明文字塞进 children，这个节点就消失、控件退化成一行文字。
  const dropzone = page.locator('.arco-upload-trigger-drag');
  await expect(dropzone).toBeVisible();
  await expect(dropzone).toContainText(/拖拽/);

  // 上传按钮不该整行铺开（曾经是一条 860px 的淡蓝色空条）
  const uploadBtn = page.getByRole('button', { name: '上传并激活' });
  await expect(uploadBtn).toBeVisible();
  const box = await uploadBtn.boundingBox();
  expect(box!.width).toBeLessThan(260);
});

test('投递调试台：未选模板时给说明而不是空白编辑器', async ({ page }) => {
  await page.goto('/integrations/deliver');
  await expect(page.locator('.df-page-title')).toContainText('投递即生成');

  // 未选模板：显示占位块，且不加载 Monaco（3.3MB 不该在首屏下）
  await expect(page.locator('.playground-empty')).toBeVisible();
  await expect(page.locator('.playground-empty-title')).toContainText('选择');
  await expect(page.locator('.monaco-editor')).toHaveCount(0);

  // 禁用按钮旁边要说明原因，不能只有一个不可点的色块
  await expect(page.getByRole('button', { name: /发送/ })).toBeDisabled();
  await expect(page.getByText('请先选择模板')).toBeVisible();
});

test('在线编辑器：docx 解析成块，点块能选中', async ({ page }) => {
  // 这条链路最长：后端下载 docx → render 服务解析成块模型 → 前端排版。
  // 任何一环断了，页面都不报错，只是空白或一直转圈——所以必须断言块的数量。
  await page.goto('/templates');
  await page.locator('.grid-card-title').first().click();
  await expect(page).toHaveURL(/\/templates\/tpl_/);

  await page.getByRole('button', { name: '在线编辑' }).click();
  await expect(page).toHaveURL(/\/editor\/tpl_/);

  await expect(page.locator('.df-editor')).toBeVisible();
  await expect(page.locator('.df-paper')).toBeVisible();

  // 解析+排版比普通页面慢，给足时间（默认 10s 在慢机器上会抖）
  const blocks = page.locator('.df-paper .df-block');
  await expect(blocks.first()).toBeVisible({ timeout: 25_000 });
  expect(await blocks.count()).toBeGreaterThan(0);

  // 点第一个块 → 出现块工具条。这条断言比"点了不报错"有用：
  // 工具条只在真的有 selectedBlock 时渲染，说明选中状态确实生效了。
  await blocks.first().click();
  await expect(page.locator('.df-block.selected')).toHaveCount(1);
  await expect(page.locator('.df-block-toolbarbar')).toBeVisible();
});

test('新建印章：填参数后出实时预览', async ({ page }) => {
  await page.goto('/seals-center/seals/new');
  await expect(page.locator('.df-page-title')).toContainText('新建印章');

  // 默认就是「参数化生成」模式，直接填三个必填项
  await page.getByPlaceholder('如：公章-2026').fill('E2E 预览用章');
  await page.getByPlaceholder('如：示例科技有限公司').fill('测试单位');
  await page.getByPlaceholder('如：合同专用章').fill('合同专用章');

  // 预览是 500ms 防抖后请求 render 服务（Go → Python），所以要等图真的出现。
  // 只等"没报错"没意义：预览失败在代码里是静默的，页面看上去一切正常。
  const previewCard = page.locator('.arco-card').filter({ hasText: '实时预览' });
  const img = previewCard.locator('img');
  await expect(img).toBeVisible({ timeout: 25_000 });

  // 图不能是 0 尺寸的占位（服务返回空 blob 时 img 仍在 DOM 里）
  const box = await img.boundingBox();
  expect(box!.width).toBeGreaterThan(50);
  expect(box!.height).toBeGreaterThan(50);

  // 有预览之后保存按钮必须可点，否则用户填完也存不下去
  await expect(page.getByRole('button', { name: '保存', exact: true })).toBeEnabled();
});

test('系统设置：保存能落库并给出回执', async ({ page }) => {
  await page.goto('/admin/settings');
  await expect(page.locator('.df-page-title')).toContainText('系统设置');

  // 只读区必须自己说清楚"不能改"，否则用户会以为页面坏了
  await expect(page.getByText('只读 · 由部署配置决定')).toBeVisible();

  // 什么都不改直接保存：验证的是"表单值能原样回写"这条链路，
  // 顺带不污染配置（改完再改回来会多一次无意义的写）。
  await page.getByRole('button', { name: '保存', exact: true }).click();
  await expect(page.getByText('已保存并即时生效')).toBeVisible({ timeout: 15_000 });
});
