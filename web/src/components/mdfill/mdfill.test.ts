import { describe, it, expect } from 'vitest';
import { buildSkeleton, nodesForNewItem, loopPaths } from './skeleton';
import type { SkeletonHeading, SkeletonNode } from './skeleton';
import { extractData, numericPaths } from './extract';
import { toDocJSON } from './toDoc';
import type { TemplateVariable } from '@/types/api';

import scannerV1 from './__fixtures__/scanner-v1.json';
import scannerV2 from './__fixtures__/scanner-v2.json';
import dataFull from './__fixtures__/data-full.json';

const V1 = scannerV1.variables as unknown as TemplateVariable[];
const V2 = scannerV2.variables as unknown as TemplateVariable[];
const FULL = dataFull as Record<string, unknown>;

function headings(nodes: SkeletonNode[]): SkeletonHeading[] {
  return nodes.filter((n): n is SkeletonHeading => n.kind === 'heading');
}

describe('骨架生成', () => {
  it('顶层标量按变量顺序出现，循环折叠成条目', () => {
    const sk = buildSkeleton(V1);
    const hs = headings(sk);

    // 12 个标量 + 2 条循环 × (1 条目标题 + 9 个子字段) = 32
    expect(hs.length).toBe(32);

    expect(hs[0].label).toBe('项目名称');
    expect(hs[0].level).toBe(2);
    expect(hs[0].slot).toBe('scalar');

    const items = hs.filter((h) => h.slot === 'item');
    expect(items.map((h) => h.label)).toEqual(['漏洞列表 1', '漏洞列表 2']);
    expect(items[0].path).toBe('漏洞列表[0]');
  });

  it('段落数组折叠成一个「多段正文」槽位，不展开成段落 1/2/3', () => {
    const sk = buildSkeleton(V1);
    const hs = headings(sk);
    const desc = hs.filter((h) => h.path.endsWith('.描述'));
    expect(desc.length).toBe(2); // 两条漏洞各一个
    expect(desc[0].slot).toBe('paragraphs');
    expect(desc[0].paraField).toBe('段落');
    expect(desc[0].label).toBe('描述');
  });

  it('单图片字段的循环折叠成「多张图片」槽位', () => {
    const sk = buildSkeleton(V1);
    const shot = headings(sk).find((h) => h.path === '漏洞列表[0].截图');
    expect(shot?.slot).toBe('images');
    expect(shot?.imageField).toBe('image_截图');
  });

  it('有实际数据时按数据条数展开，不按模板示例条数', () => {
    // 模板示例是 2 条，真实报告有 4 条
    const sk = buildSkeleton(V1, FULL);
    const items = headings(sk).filter((h) => h.slot === 'item');
    expect(items.length).toBe(4);
    expect(items[3].path).toBe('漏洞列表[3]');
  });

  it('v2 与 v1 字段一致（v2 多报告日期，是标量不额外展开）', () => {
    const a = headings(buildSkeleton(V1)).map((h) => h.label);
    const b = headings(buildSkeleton(V2)).map((h) => h.label);
    expect(b.length).toBe(a.length);
    expect(b).toContain('报告日期');
  });

  it('枚举字段带上可选值与提示', () => {
    // 用一份人造变量树：扫描器在模板里出现 {% if 风险等级 == "高" %} 时会给 enum
    const vars = [
      { name: '风险等级', kind: 'scalar', path: '风险等级', required: true, enum: ['高', '中', '低'] },
    ] as unknown as TemplateVariable[];
    const h = headings(buildSkeleton(vars))[0];
    expect(h.enum).toEqual(['高', '中', '低']);
    expect(h.hint).toContain('高 / 中 / 低');
  });

  it('条件变量不出现在文档里', () => {
    const vars = [
      { name: '是否加急', kind: 'condition', path: '是否加急', required: false },
    ] as unknown as TemplateVariable[];
    expect(buildSkeleton(vars).length).toBe(0);
  });

  it('服务端签章变量不让人填', () => {
    const vars = [
      { name: '签名', kind: 'image', path: '签名', required: false, system_resolved: true },
    ] as unknown as TemplateVariable[];
    expect(buildSkeleton(vars).length).toBe(0);
  });
});

describe('数据往返：data → 文档 → data', () => {
  it('真实报告数据往返后完全一致', () => {
    const sk = buildSkeleton(V1, FULL);
    const doc = toDocJSON(sk, FULL);
    const { data, issues } = extractData(doc, numericPaths(FULL), FULL);

    expect(issues.filter((i) => i.level === 'error')).toEqual([]);
    expect(data).toEqual(FULL);
  });

  it('二次往返仍然一致（幂等，避免每次保存都漂移）', () => {
    const sk1 = buildSkeleton(V1, FULL);
    const once = extractData(toDocJSON(sk1, FULL), numericPaths(FULL), FULL).data;
    const sk2 = buildSkeleton(V1, once);
    const twice = extractData(toDocJSON(sk2, once), numericPaths(once), once).data;
    expect(twice).toEqual(once);
  });

  it('v2 模板同样往返一致', () => {
    const sk = buildSkeleton(V2, FULL);
    const doc = toDocJSON(sk, FULL);
    const { data, issues } = extractData(doc, numericPaths(FULL), FULL);
    expect(issues.filter((i) => i.level === 'error')).toEqual([]);
    expect(data).toEqual(FULL);
  });

  it('空数据得到空骨架，不产生任何字段值', () => {
    const sk = buildSkeleton(V1);
    const doc = toDocJSON(sk);
    const { data } = extractData(doc);
    // 空字符串与空数组都不该落进 data
    expect(data).toEqual({});
  });

  it('多段落严格保持段数，空段落被丢掉', () => {
    const src = {
      漏洞列表: [
        {
          编号: '1',
          名称: '未授权',
          等级: '高危',
          影响范围: 'a',
          漏洞地址: 'b',
          描述: [{ 段落: '第一段' }, { 段落: '第二段' }, { 段落: '第三段' }],
          详情: [],
          建议: [],
          截图: [],
        },
      ],
    };
    const sk = buildSkeleton(V1, src);
    const { data } = extractData(toDocJSON(sk, src));
    const list = (data as any).漏洞列表;
    expect(list[0].描述).toEqual([{ 段落: '第一段' }, { 段落: '第二段' }, { 段落: '第三段' }]);
  });

  it('正文里的 & < > 不会破坏结构', () => {
    const src = {
      项目名称: 'A&B <测试> 公司',
      系统备注: 'R&D 部门，版本 <v2>',
    };
    const sk = buildSkeleton(V1, src);
    const { data } = extractData(toDocJSON(sk, src));
    expect((data as any).项目名称).toBe('A&B <测试> 公司');
    expect((data as any).系统备注).toBe('R&D 部门，版本 <v2>');
  });

  it('数字类型在往返里保持数字', () => {
    const src = { 系统数量: 3, 高危数量: 1 };
    const sk = buildSkeleton(V1, src);
    const { data } = extractData(toDocJSON(sk, src), numericPaths(src));
    expect((data as any).系统数量).toBe(3);
    expect((data as any).高危数量).toBe(1);
  });

  it('全空的循环条目不会留在结果里', () => {
    const src = {
      漏洞列表: [
        { 编号: '1', 名称: 'A', 等级: '高危', 影响范围: '', 漏洞地址: '', 描述: [], 详情: [], 建议: [], 截图: [] },
        { 编号: '', 名称: '', 等级: '', 影响范围: '', 漏洞地址: '', 描述: [], 详情: [], 建议: [], 截图: [] },
      ],
    };
    const sk = buildSkeleton(V1, src);
    const { data } = extractData(toDocJSON(sk, src));
    expect((data as any).漏洞列表.length).toBe(1);
  });
});

describe('校验提示', () => {
  it('必填项空着会报 error', () => {
    const sk = buildSkeleton(V1);
    const { issues } = extractData(toDocJSON(sk));
    const errs = issues.filter((i) => i.level === 'error');
    expect(errs.length).toBeGreaterThan(0);
    expect(errs.some((e) => e.label === '项目名称')).toBe(true);
  });

  it('枚举值写错会报 error 并给出可选值', () => {
    const vars = [
      { name: '风险等级', kind: 'scalar', path: '风险等级', required: true, enum: ['高', '中', '低'] },
    ] as unknown as TemplateVariable[];
    const sk = buildSkeleton(vars);
    const { issues } = extractData(toDocJSON(sk, { 风险等级: '高风险' }));
    const err = issues.find((i) => i.label === '风险等级');
    expect(err?.level).toBe('error');
    expect(err?.message).toContain('高');
  });

  it('标量写了多段会给出提醒', () => {
    const vars = [{ name: '项目名称', kind: 'scalar', path: '项目名称', required: true }] as unknown as TemplateVariable[];
    const sk = buildSkeleton(vars);
    const { issues } = extractData(toDocJSON(sk, { 项目名称: '第一行\n第二行' }));
    expect(issues.some((i) => i.level === 'warn' && i.label === '项目名称')).toBe(true);
  });
});

describe('下标按出现顺序重编号', () => {
  /** 手工拼一份「删过中间一条」的文档：路径写着 [0] 和 [2]，中间有洞。 */
  const docWithHole = {
    type: 'doc',
    content: [
      { type: 'fillHeading', attrs: { slot: 'item', itemPath: '漏洞列表', level: 2, path: '漏洞列表[0]', label: '漏洞列表 1' } },
      { type: 'fillHeading', attrs: { slot: 'scalar', path: '漏洞列表[0].名称', level: 3, label: '名称', required: true } },
      { type: 'paragraph', content: [{ type: 'text', text: '第一条' }] },
      { type: 'fillHeading', attrs: { slot: 'item', itemPath: '漏洞列表', level: 2, path: '漏洞列表[2]', label: '漏洞列表 3' } },
      { type: 'fillHeading', attrs: { slot: 'scalar', path: '漏洞列表[2].名称', level: 3, label: '名称', required: true } },
      { type: 'paragraph', content: [{ type: 'text', text: '第三条' }] },
    ],
  };

  it('路径里的空洞不会带到结果里，Jinja 遍历不会踩到 undefined', () => {
    const { data } = extractData(docWithHole);
    const list = (data as any).漏洞列表;
    expect(list.length).toBe(2);
    expect(list[0].名称).toBe('第一条');
    expect(list[1].名称).toBe('第三条');
  });

  it('嵌套层级的 path 也跟着重编号', () => {
    const doc = {
      type: 'doc',
      content: [
        { type: 'fillHeading', attrs: { slot: 'item', itemPath: '漏洞列表', level: 2, path: '漏洞列表[5]' } },
        { type: 'fillHeading', attrs: { slot: 'paragraphs', path: '漏洞列表[5].描述', level: 3, label: '描述', paraField: '段落' } },
        { type: 'paragraph', content: [{ type: 'text', text: '描述一' }] },
      ],
    };
    const { data } = extractData(doc);
    expect((data as any).漏洞列表[0].描述).toEqual([{ 段落: '描述一' }]);
  });

  it('同层新条目会顶掉栈顶，不会把子字段挂到上一条上', () => {
    const doc = {
      type: 'doc',
      content: [
        { type: 'fillHeading', attrs: { slot: 'item', itemPath: '漏洞列表', level: 2, path: '漏洞列表[0]' } },
        { type: 'fillHeading', attrs: { slot: 'item', itemPath: '漏洞列表', level: 2, path: '漏洞列表[1]' } },
        { type: 'fillHeading', attrs: { slot: 'scalar', path: '漏洞列表[1].编号', level: 3, label: '编号' } },
        { type: 'paragraph', content: [{ type: 'text', text: '2' }] },
      ],
    };
    const { data } = extractData(doc);
    // 第一条只有标题、没有内容：产品上不该在导出结果里留一行空表格
    expect((data as any).漏洞列表.length).toBe(1);
    expect((data as any).漏洞列表[0].编号).toBe('2');
  });

  it('nodesForNewItem 生成新条目所需的全部节点', () => {
    const loop = headings(buildSkeleton(V1)).find((h) => h.slot === 'item')!.loopVar!;
    const nodes = nodesForNewItem(loop, 2, 5);
    expect(nodes.length).toBe(10); // 1 条目标题 + 9 个子字段
    const labels = headings(nodes).map((h) => h.label);
    expect(labels[0]).toBe('漏洞列表 6');
    expect(labels).toContain('描述');
    expect(labels).toContain('截图');
  });

  it('loopPaths 能列出所有可增删的循环', () => {
    const lp = loopPaths(buildSkeleton(V1));
    expect(lp.length).toBe(1);
    expect(lp[0].path).toBe('漏洞列表');
    expect(lp[0].count).toBe(2);
  });

  /**
   * 这一条守的是一个**隐含约定**：编辑器里所有「找某个骨架标题在哪」的代码
   * （applyEnum 填枚举值、removeItem 删条目、insertItem 加条目、insertImage
   * 插图片）都是 `doc.forEach` 顶层遍历。
   *
   * 顶层遍历能成立，前提是骨架标题全在文档第一层——目前确实如此，因为骨架是
   * 从变量树展平出来的。但这个前提一旦被后来的改动打破（比如哪天把标题塞进
   * 表格单元格里排版），那些遍历会**静默地一个都找不到**：点枚举没反应、
   * 加条目没反应，而且不报错、不抛异常，只是「点了没用」。
   *
   * 与其等到那时靠人肉发现，不如把它钉成断言。
   */
  it('全部骨架标题都在文档顶层（位置计算依赖这一点）', () => {
    const sk = buildSkeleton(V1);
    const doc = toDocJSON(sk, undefined) as any;
    const heads = headings(sk).length;
    expect(heads).toBeGreaterThan(0);

    const topLevel = (doc.content || []).filter((n: any) => n.type === 'fillHeading');
    expect(topLevel.length).toBe(heads);

    // 再确认没有藏在别的节点里（表格、引用块之类）
    let nested = 0;
    const walk = (n: any, depth: number) => {
      if (n.type === 'fillHeading' && depth > 0) nested++;
      (n.content || []).forEach((c: any) => walk(c, depth + 1));
    };
    (doc.content || []).forEach((n: any) => walk(n, 0));
    expect(nested).toBe(0);
  });
});
