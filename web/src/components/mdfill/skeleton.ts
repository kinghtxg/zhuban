import type { TemplateVariable, VarKind } from '@/types/api';
import type { ChartPayload } from '@/components/ChartDataDrawer';

/**
 * 模板变量树 → 骨架文档模型。
 *
 * 这一步不碰编辑器，只把「模板要什么」翻译成「文档长什么样」。
 * 纯函数，可以脱离 React 与 ProseMirror 单独测。
 *
 * 为什么要有这一层：模板的变量树是**扁平清单**（扫描器把嵌套循环摊平成
 * fields 数组），而用户要填的是一份**有章节顺序的文档**。中间缺一层翻译，
 * 界面就只能退化成「一堆输入框」——那正是现在问卷式表单的毛病：
 * 用户看见「漏洞列表[0].严重等级」，不知道它在文档第几章。
 *
 * 翻译规则（对着扫描器的实际产物设计，见 render-service/app/scanner.py）：
 *
 *   标量         → 一个标题 + 标题下的正文
 *   富文本       → 同上，但正文按 Markdown 存
 *   图片         → 一个标题 + 图片位
 *   循环（多字段）→ 每个条目一组标题：`漏洞 1` 下挂 `编号/名称/等级…`
 *   循环（单标量字段）→ 折叠成「多段正文」。扫描器把
 *                  `{%p for 描述段 in 漏洞.描述 %}{{ 描述段.段落 }}` 解读成
 *                  一个单字段子循环；用户心里那东西不是「数组」，是
 *                  「描述这一段可以写好几段话」。展开成 N 个「段落 1/2/3」
 *                  纯属把实现细节摊给用户看。
 *   循环（单图片字段）→ 折叠成「多张图片」，同理。
 *   条件         → 不渲染。它是 `{% if %}` 的开关，不是用户该填的值；
 *                  让它出现在文档里，等于逼用户理解 Jinja。
 *   图表         → 独立节点，正文位置放一个可编辑的 JSON 块。
 */

/** 标题下那一块可编辑区域该按什么形态渲染。 */
export type SlotKind =
  | 'scalar' // 一行字符串
  | 'paragraphs' // 多个段落，合成 [{字段: 文本}]
  | 'richtext' // Markdown 富文本
  | 'image' // 一张图片
  | 'images' // 多张图片
  | 'qrcode' // 二维码
  | 'chart' // 图表：正文位置放一个 JSON 代码块
  | 'item'; // 循环条目标题，本身不可填，只是分组

export interface SkeletonHeading {
  kind: 'heading';
  /** 数据路径，如 `漏洞列表[0].描述`。提取时靠它把正文归位。 */
  path: string;
  /** 显示名。 */
  label: string;
  /** 2 = 顶层字段；3 = 循环项内部字段；更深用于嵌套循环。 */
  level: number;
  slot: SlotKind;
  /** 灰色提示：枚举可选值、多段说明之类。 */
  hint?: string;
  required?: boolean;
  enum?: string[];
  /** slot=paragraphs 时：每段写进这个子字段名（扫描器给的，通常是「段落」）。 */
  paraField?: string;
  /** slot=image/images 时：图片写进这个子字段名（通常是 `image_截图`）。 */
  imageField?: string;
  /** slot=item 时：所属循环的数组名与序号，供增删条目用。 */
  itemPath?: string;
  itemIndex?: number;
  /** 所属循环的原始变量定义，用于「再插一条」。 */
  loopVar?: TemplateVariable;
}

export interface SkeletonChart {
  kind: 'chart';
  path: string;
  label: string;
  chartKind: string;
  title: string;
  seriesCount: number;
  pointCount: number;
  payload: ChartPayload;
}

export type SkeletonNode = SkeletonHeading | SkeletonChart;

/** 段落类子字段的常见名字。扫描器按 `{{ 描述段.段落 }}` 的字段名原样记账。 */
const PARA_FIELD_HINTS = ['段落', '文本', '内容', '正文', 'paragraph', 'text'];

function looksLikeParagraphField(name: string): boolean {
  const n = name.trim().toLowerCase();
  return PARA_FIELD_HINTS.some((h) => n === h.toLowerCase() || n.endsWith(h.toLowerCase()));
}

/** 把一个循环折叠成「多段正文」的条件：只有一个标量子字段，且字段名像段落。 */
function asParagraphLoop(v: TemplateVariable): string | null {
  const fields = v.fields || [];
  if (fields.length !== 1) return null;
  const f = fields[0];
  if (f.kind !== 'scalar') return null;
  return looksLikeParagraphField(f.name) ? f.name : null;
}

/** 折叠成「多张图片」的条件：只有一个图片子字段。 */
function asImageLoop(v: TemplateVariable): string | null {
  const fields = v.fields || [];
  if (fields.length !== 1) return null;
  const f = fields[0];
  return f.kind === 'image' ? f.name : null;
}

function enumHint(enumVals?: string[]): string | undefined {
  if (!enumVals || enumVals.length === 0) return undefined;
  return `可选：${enumVals.join(' / ')}`;
}

/** 按 `漏洞列表[0].描述` 这样的路径取值。取不到返回 undefined。 */
function readPath(root: unknown, path: string): unknown {
  let cur: any = root;
  for (const seg of path.split('.')) {
    if (cur == null) return undefined;
    const m = seg.match(/^([^[\]]+)(?:\[(\d+)\])?$/);
    if (!m) return undefined;
    cur = cur[m[1]];
    if (m[2] !== undefined) {
      if (!Array.isArray(cur)) return undefined;
      cur = cur[Number(m[2])];
    }
  }
  return cur;
}

/**
 * 取一条循环的展开条数。
 *
 * 优先看**已有数据**里这个数组有多长：扫描器给的 sample_size 是模板里示例的
 * 条数（通常是 2），而用户手上那份报告可能已经有 4 条漏洞。按示例条数展开，
 * 打开页面看到的条目就不对，第 3、4 条的内容会无处可去。
 * 没有数据时才退回 sample_size。
 */
function itemCount(v: TemplateVariable, path: string, data?: Record<string, unknown>): number {
  const existing = data ? readPath(data, path) : undefined;
  if (Array.isArray(existing) && existing.length > 0) return existing.length;
  const n = v.sample_size;
  if (typeof n === 'number' && n > 0) return n;
  return 1;
}

function headingFor(v: TemplateVariable, level: number, path: string): SkeletonHeading {
  const base = { kind: 'heading' as const, path, label: v.name, level, required: v.required };

  switch (v.kind) {
    case 'image':
      return { ...base, slot: 'image', hint: '把鼠标移到这一行，点「添加图片」' };
    case 'qrcode':
      return { ...base, slot: 'qrcode', hint: '填一段文本，服务端生成二维码' };
    case 'richtext':
      return { ...base, slot: 'richtext', hint: '支持加粗、列表' };
    case 'loop': {
      const paraField = asParagraphLoop(v);
      if (paraField) {
        // 提示必须说清「回车」而不是「空行」。用户按一次回车就新起一段，
        // 按两次会多出一个空段落（提取时被过滤掉，不报错但也没意义）。
        // 写成「空行分段」会让他以为要按两次，结果段与段之间多一行空白。
        return { ...base, slot: 'paragraphs', paraField, hint: '按回车新起一段，段与段会分开' };
      }
      const imageField = asImageLoop(v);
      if (imageField) {
        return { ...base, slot: 'images', imageField, hint: '可放多张，按顺序拼进文档' };
      }
      // 走不到这里：多字段循环由调用方展开成条目
      return { ...base, slot: 'scalar' };
    }
    default:
      return { ...base, slot: 'scalar', enum: v.enum, hint: enumHint(v.enum) };
  }
}

/**
 * 展开一个字段（含其子结构）到 out 里。
 *
 * `level` 是该字段标题的级别；循环条目本身占一级，其内部字段再降一级。
 */
function walk(
  v: TemplateVariable,
  level: number,
  parentPath: string,
  out: SkeletonNode[],
  data?: Record<string, unknown>
) {
  const path = parentPath ? `${parentPath}.${v.name}` : v.name;

  if (v.kind === 'condition') return; // 条件开关不进文档

  if (v.kind === 'chart') {
    out.push(chartNode(v, path));
    return;
  }

  if (v.kind === 'loop' && !asParagraphLoop(v) && !asImageLoop(v)) {
    // 真正的条目循环：展开 N 条，每条一组子标题
    const n = itemCount(v, path, data);
    for (let i = 0; i < n; i++) {
      out.push({
        kind: 'heading',
        path: `${path}[${i}]`,
        label: `${v.name} ${i + 1}`,
        level,
        slot: 'item',
        itemPath: path,
        itemIndex: i,
        loopVar: v,
      });
      for (const f of v.fields || []) {
        walk(f, level + 1, `${path}[${i}]`, out, data);
      }
    }
    return;
  }

  out.push(headingFor(v, level, path));
}

function chartNode(v: TemplateVariable, path: string): SkeletonChart {
  const payload = chartPayloadFromSample(v);
  return {
    kind: 'chart',
    path,
    label: v.name,
    chartKind: v.chart_kind || 'bar',
    title: v.title || v.name,
    seriesCount: v.series_count ?? payload.series.length,
    pointCount: v.point_count ?? payload.categories.length,
    payload,
  };
}

/**
 * 从变量的 sample 里读出图表初始数据。
 *
 * 图表变量没有 `{{ }}` 可放，扫描器给的是整个 `{categories, series}` 结构，
 * 塞在 sample 里。读不出来就给空骨架（保留系列数量，值为 0）——系列数不能丢，
 * 丢了渲染侧就没法把数据写回正确的 `c:ser` 次序。
 */
function chartPayloadFromSample(v: TemplateVariable): ChartPayload {
  const s = v.sample as ChartPayload | undefined;
  if (s && Array.isArray(s.categories) && Array.isArray(s.series)) {
    return {
      categories: s.categories.map(String),
      series: s.series.map((x) => ({
        name: String(x?.name ?? ''),
        values: (x?.values || []).map((n) => Number(n) || 0),
      })),
    };
  }
  const cats = Array.from({ length: Math.max(v.point_count ?? 3, 1) }, (_, i) => `项 ${i + 1}`);
  const series = Array.from({ length: Math.max(v.series_count ?? 1, 1) }, (_, i) => ({
    name: `系列 ${i + 1}`,
    values: cats.map(() => 0),
  }));
  return { categories: cats, series };
}

/**
 * 生成骨架。
 *
 * 顶层标题从 level 2 起：level 1 留给文档标题（模板名），是一个不可编辑的
 * 装饰标题。这样「第一个 h1 是文档标题」这类常见约定不会被字段名冲掉。
 */
export function buildSkeleton(vars: TemplateVariable[], data?: Record<string, unknown>): SkeletonNode[] {
  const out: SkeletonNode[] = [];
  for (const v of vars) {
    if (v.system_resolved) continue; // 服务端签章注入的，不让人填
    walk(v, 2, '', out, data);
  }
  return out;
}

/**
 * 在骨架里给某个循环追加一条。
 *
 * 返回新的骨架数组。插入位置是「该循环最后一条的最后一个子字段之后」——
 * 直接追加到末尾会把「漏洞 3」放到「系统备注」下面，看起来像另一个字段。
 */
/**
 * 为一个循环条目生成骨架节点组（条目标题 + 它的全部子字段）。
 *
 * 增删条目是**文档级操作**（在 ProseMirror 里插入/删除一组节点），不走
 * 「重建整份骨架」——重建会把用户已经写进去的正文全部冲掉。这个函数只负责
 * 造出「新的一条长什么样」，插到文档哪个位置由编辑器决定。
 *
 * 下标只作提示：extract 按出现顺序重新编号，所以这里给哪个数不影响正确性，
 * 给对了只是为了调试时对得上。
 */
export function nodesForNewItem(loopVar: TemplateVariable, level: number, index: number): SkeletonNode[] {
  const loopPath = loopVar.name;
  const out: SkeletonNode[] = [
    {
      kind: 'heading',
      path: `${loopPath}[${index}]`,
      label: `${(loopVar.name ?? '').trim()} ${index + 1}`,
      level,
      slot: 'item',
      itemPath: loopPath,
      itemIndex: index,
      loopVar,
    },
  ];
  for (const f of loopVar.fields || []) {
    walk(f, level + 1, `${loopPath}[${index}]`, out);
  }
  return out;
}

export function loopPaths(nodes: SkeletonNode[]): { path: string; label: string; count: number }[] {
  const seen = new Map<string, { label: string; count: number }>();
  for (const n of nodes) {
    if (n.kind !== 'heading' || n.slot !== 'item' || !n.itemPath) continue;
    const cur = seen.get(n.itemPath);
    const label = (n.loopVar?.name ?? n.itemPath).trim();
    seen.set(n.itemPath, { label, count: (cur?.count ?? 0) + 1 });
  }
  return [...seen.entries()].map(([path, v]) => ({ path, ...v }));
}

export type { VarKind };
