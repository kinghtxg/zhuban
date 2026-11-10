/**
 * 编辑器文档 → data JSON。
 *
 * 这是「Markdown 化填写」能不能成立的关键一步：用户在文档里写的东西，
 * 必须**无损地**变回渲染服务要的那个 data 对象。这里的每一条规则都对着
 * 一个具体的数据形状（见 render-service/app/scanner.py 的变量产物）。
 *
 * 文档结构是扁平的：
 *
 *     [fillHeading 项目名称]  [paragraph 尧天灾备云]
 *     [fillHeading 漏洞 1]    [fillHeading 编号] [paragraph 1] …
 *
 * 提取时遍历顶层节点，遇到 fillHeading 就切换「当前字段」，其后的内容节点
 * 归给这个字段，直到下一个 fillHeading。不做嵌套是因为**嵌套会让删除变难**：
 * 用户删掉中间一段时，ProseMirror 要处理跨层级的事务，出错就是整块内容消失。
 * 扁平结构下删内容只是删节点，字段的归属关系由标题的 level 隐式表达。
 *
 * ★ 下标按**出现顺序**重新编排，不信 attrs 里写的 `[i]`。
 *
 * 用户删掉「漏洞 2」之后，文档里剩下的是 `[0]` 和 `[2]` 两段，中间有个洞。
 * 如果照 attrs 提取，data 里会是 `漏洞列表[0]` 与 `漏洞列表[2]`，Jinja 的
 * `{% for 漏洞 in 漏洞列表 %}` 读到中间那种 undefined 直接抛错，整份报告渲染失败。
 * 而让「删一条」去把所有后续条目的 attrs 全部重写一遍，代价是每次删除都要
 * 扫全文档打补丁——漏一处就是错位，且这种错位在界面上完全看不出来。
 *
 * 所以：文档里 attrs 的路径只当「归属标签」，真实下标由遍历顺序决定。
 */

import type { SlotKind } from './skeleton';

/** 提取过程中发现的问题，显示在「预览数据」面板里。 */
export interface ExtractIssue {
  level: 'error' | 'warn';
  path: string;
  label: string;
  message: string;
}

export interface ExtractResult {
  data: Record<string, unknown>;
  issues: ExtractIssue[];
}

/** 图片值的形状，和 FillForm / 渲染服务的约定一致。 */
export interface ImageValue {
  type: 'image';
  src: string;
  width?: number;
  height?: number;
}

function isBlankText(s: string): boolean {
  return s.replace(/\u00a0/g, ' ').trim() === '';
}

/** 把一个文本块节点的纯文本取出来。 */
function textOfNode(node: any): string {
  if (!node) return '';
  if (node.type === 'text') return String(node.text ?? '');
  if (node.type === 'hardBreak') return '\n';
  if (Array.isArray(node.content)) return node.content.map(textOfNode).join('');
  return '';
}

function splitPath(path: string): { name: string; index?: number }[] {
  const out: { name: string; index?: number }[] = [];
  for (const seg of path.split('.')) {
    const m = seg.match(/^([^[\]]+)(?:\[(\d+)\])?$/);
    out.push(m ? { name: m[1], index: m[2] === undefined ? undefined : Number(m[2]) } : { name: seg });
  }
  return out;
}

/** 按 path 把值写进嵌套结构，沿途按需创建数组/对象。 */
function setPath(root: Record<string, unknown>, path: string, value: unknown) {
  const segs = splitPath(path);
  let cur: any = root;
  for (let i = 0; i < segs.length; i++) {
    const { name, index } = segs[i];
    const last = i === segs.length - 1;

    if (index === undefined) {
      if (last) {
        cur[name] = value;
        return;
      }
      if (cur[name] == null || typeof cur[name] !== 'object') cur[name] = {};
      cur = cur[name];
      continue;
    }

    if (!Array.isArray(cur[name])) cur[name] = [];
    const arr = cur[name] as any[];
    while (arr.length <= index) arr.push({});
    if (last) {
      arr[index] = value;
      return;
    }
    if (arr[index] == null || typeof arr[index] !== 'object') arr[index] = {};
    cur = arr[index];
  }
}

function getPath(root: Record<string, unknown>, path: string): unknown {
  let cur: any = root;
  for (const { name, index } of splitPath(path)) {
    if (cur == null) return undefined;
    cur = index === undefined ? cur[name] : cur[name]?.[index];
  }
  return cur;
}

/** 一个字段在文档里累积到的原始内容。 */
interface Bucket {
  /** 标题的原始 attrs。path 在 flush 时才解析成真实下标。 */
  attrs: Record<string, any>;
  /**
   * 入桶那一刻的条目栈快照。
   *
   * 必须快照：bucket 是在**遇到下一个标题时**才 flush 的，而那时栈已经指向
   * 下一条了。直接读当前栈，第一条漏洞的全部字段都会被记到第二条名下——
   * 症状是「只有最后一条有内容」，且前几条在界面上明明填过。
   */
  stack: ItemCtx[];
  paras: string[];
  html: string[];
  images: string[];
  code: string[];
}

/** 条目上下文：当前处在哪个循环的第几条。 */
interface ItemCtx {
  loop: string;
  index: number;
  level: number;
}

/**
 * 把 attrs 里的 `循环名[旧下标]` 换成实际顺序下标。
 *
 * 一层一层替换：嵌套循环时栈里同时有外层与内层，两段都要换。
 */
function resolvePath(path: string, stack: ItemCtx[]): string {
  let p = path;
  for (const c of stack) {
    const prefix = `${c.loop}[`;
    if (!p.startsWith(prefix)) continue;
    const close = p.indexOf(']', prefix.length);
    if (close < 0) continue;
    p = `${c.loop}[${c.index}]` + p.slice(close + 1);
  }
  return p;
}

function slotOf(b: Bucket): SlotKind {
  return (b.attrs.slot ?? 'scalar') as SlotKind;
}

/**
 * 一个字段的最终值。各槽位的规则：
 *
 *   scalar      多个段落用换行连接。本意是单行字段，但用户按住 Shift+回车
 *               硬折行时不该把他的字吞掉——内容层面的问题交给「预览数据」
 *               去提示，这里先原样保住。
 *   paragraphs  每个非空段落一项 `{[paraField]: 文本}`。空段落丢掉：
 *               用户在段间多按一次回车是常态，那不是「要一个空段落」。
 *   richtext    段落 HTML 拼起来，保持原有行内格式。
 *   image/images 按出现顺序，保持 `{type:'image',src}` 形状。
 *   item        本身不产值（由子字段填进所属条目）。
 */
function valueOf(
  b: Bucket,
  keepAsNumber: (path: string) => boolean,
  path: string,
  keepEmpty: (path: string) => boolean
): unknown {
  const slot = slotOf(b);
  const paras = b.paras.filter((t) => !isBlankText(t));

  switch (slot) {
    case 'item':
      return undefined;
    case 'paragraphs': {
      const field = b.attrs.paraField || '段落';
      return paras.map((t) => ({ [field]: t }));
    }
    case 'richtext': {
      const html = b.html.filter((t) => !isBlankText(t));
      return { type: 'markdown', content: html.join('\n') };
    }
    case 'image': {
      const src = b.images[0];
      return src ? ({ type: 'image', src } as ImageValue) : undefined;
    }
    case 'images': {
      const field = b.attrs.imageField || 'image';
      return b.images.map((src) => ({ [field]: { type: 'image', src } as ImageValue }));
    }
    case 'qrcode': {
      const t = paras.join('\n');
      return t ? { type: 'qrcode', text: t, size: 20 } : undefined;
    }
    case 'chart': {
      // 图表数据写在代码块里，原样交给上层解析（解析失败由上层报错）
      const raw = b.code.join('\n').trim();
      return raw || undefined;
    }
    default: {
      const joined = paras.join('\n');
      // 原来是空字符串的字段，空着也要交回一个空字符串（见 presentPaths 的说明）
      if (!joined) return keepEmpty(path) ? '' : undefined;
      // number 类型的原值在往返里保持 number：`系统数量` 写成 "1" 也没错，
      // 但用户从示例 JSON 里拿到的若是 1，回传字符串就是「悄悄改了类型」。
      if (keepAsNumber(path) && /^-?\d+(\.\d+)?$/.test(joined.trim())) {
        return Number(joined.trim());
      }
      return joined;
    }
  }
}

/**
 * 从示例数据里记下哪些 path 原本是数字，往返时保持类型。
 */
export function numericPaths(sample: Record<string, unknown> | undefined): Set<string> {
  const out = new Set<string>();
  if (!sample) return out;
  const walk = (obj: any, prefix: string) => {
    if (Array.isArray(obj)) {
      obj.forEach((v, i) => walk(v, `${prefix}[${i}]`));
      return;
    }
    if (obj && typeof obj === 'object') {
      for (const [k, v] of Object.entries(obj)) {
        const p = prefix ? `${prefix}.${k}` : k;
        if (typeof v === 'number') out.add(p);
        else if (typeof v === 'object') walk(v, p);
      }
    }
  };
  walk(sample, '');
  return out;
}

/**
 * 从原始数据里记下「本来就存在、但值是空」的路径。
 *
 * 为什么需要：真实报告里 `系统备注` 常常就是一个空字符串——它存在、合法、
 * 服务端也接受。如果往返时把空值当成「没填」丢掉，用户打开已有草稿再原样
 * 提交，data 就少了一个键，必填校验立刻报红，而他什么都没改。
 *
 * 空值保不保留只能由「原来有没有」决定，不能由「现在是不是空的」决定。
 */
export function presentPaths(sample: Record<string, unknown> | undefined): {
  emptyScalars: Set<string>;
  arrayKeys: Set<string>;
} {
  const emptyScalars = new Set<string>();
  const arrayKeys = new Set<string>();
  if (!sample) return { emptyScalars, arrayKeys };

  const walk = (obj: any, prefix: string) => {
    if (Array.isArray(obj)) {
      if (prefix) arrayKeys.add(prefix);
      obj.forEach((v, i) => walk(v, `${prefix}[${i}]`));
      return;
    }
    if (obj && typeof obj === 'object') {
      for (const [k, v] of Object.entries(obj)) {
        const p = prefix ? `${prefix}.${k}` : k;
        if (v && typeof v === 'object') walk(v, p);
        else if (v === '' || v == null) emptyScalars.add(p);
      }
      return;
    }
    if (obj === '') emptyScalars.add(prefix);
  };
  walk(sample, '');
  return { emptyScalars, arrayKeys };
}

/**
 * 主提取函数。
 *
 * `docJSON` 是 ProseMirror 的 `editor.getJSON()`。
 * `keepNumber` 是 numericPaths 的结果，决定标量回传字符串还是数字。
 */
export function extractData(
  docJSON: any,
  keepNumber?: Set<string>,
  seed?: Record<string, unknown>
): ExtractResult {
  const data: Record<string, unknown> = {};
  const issues: ExtractIssue[] = [];
  const buckets: Bucket[] = [];

  const counters = new Map<string, number>();
  const stack: ItemCtx[] = [];

  let cur: Bucket | null = null;
  const flush = () => {
    if (cur) buckets.push(cur);
    cur = null;
  };

  for (const node of docJSON?.content ?? []) {
    if (node.type === 'fillHeading') {
      flush();
      const attrs = node.attrs ?? {};

      if (attrs.slot === 'item') {
        const loop = String(attrs.itemPath ?? attrs.path ?? '');
        const level = Number(attrs.level ?? 2);
        // 同层或更浅的条目先出栈：level 越小越是外层
        while (stack.length > 0 && stack[stack.length - 1].level >= level) stack.pop();
        const index = counters.get(loop) ?? 0;
        counters.set(loop, index + 1);
        stack.push({ loop, index, level });
      }

      cur = { attrs, stack: stack.map((s) => ({ ...s })), paras: [], html: [], images: [], code: [] };
      continue;
    }
    if (!cur) continue; // 标题之前的内容（不该出现，出现就忽略）

    if (node.type === 'paragraph') {
      cur.paras.push(textOfNode(node));
      cur.html.push(paragraphHTML(node));
    } else if (node.type === 'image') {
      const src = String(node.attrs?.src ?? '');
      if (src) cur.images.push(src);
    } else if (node.type === 'codeBlock') {
      cur.code.push(textOfNode(node));
    } else if (
      node.type === 'bulletList' ||
      node.type === 'orderedList' ||
      node.type === 'table' ||
      node.type === 'taskList'
    ) {
      // 列表/表格在标量字段里没有对应的数据形状。收下它的文本，
      // 丢掉结构——比整段消失要好，同时报一条提示让用户知道。
      cur.paras.push(textOfNode(node));
      issues.push({
        level: 'warn',
        path: String(cur.attrs.path ?? ''),
        label: String(cur.attrs.label ?? ''),
        message: '这里用了列表或表格，导出时会压成纯文本（模板里没有对应结构）',
      });
    }
  }
  flush();

  const keepNum = (p: string) => (keepNumber ? keepNumber.has(p) : false);
  const { emptyScalars, arrayKeys } = presentPaths(seed);
  const keepEmpty = (p: string) => emptyScalars.has(p);

  for (const b of buckets) {
    const slot = slotOf(b);
    const label = String(b.attrs.label ?? '');
    const rawPath = String(b.attrs.path ?? '');

    if (slot === 'item') {
      const loop = String(b.attrs.itemPath ?? rawPath);
      const idx = stackIndexFor(b.stack, loop);
      ensureItem(data, loop, idx);
      continue;
    }

    const path = resolvePath(rawPath, b.stack);
    const v = valueOf(b, keepNum, path, keepEmpty);

    if (v !== undefined) {
      if (slot === 'chart') {
        const parsed = parseChartJSON(String(v));
        if (parsed.error) {
          issues.push({ level: 'error', path, label, message: parsed.error });
        } else {
          setPath(data, path, parsed.value);
        }
      } else {
        setPath(data, path, v);
      }
    } else if (b.attrs.required) {
      issues.push({ level: 'error', path, label, message: '必填，还没填' });
    }

    const enumVals: string[] | null = Array.isArray(b.attrs.enum) ? b.attrs.enum : null;
    if (enumVals && enumVals.length > 0) {
      const got = typeof v === 'string' ? v.trim() : '';
      if (got && !enumVals.includes(got)) {
        issues.push({
          level: 'error',
          path,
          label,
          message: `「${got}」不在可选值里（${enumVals.join(' / ')}）`,
        });
      }
    }

    if (slot === 'scalar' && b.paras.some((t) => t.includes('\n'))) {
      // 硬折行（Shift+回车）也算：数据里这个字段就是一个字符串，
      // 换行会被原样塞进 docx 的同一个 run，Word 里看起来是「一行里挤了两行」。
      issues.push({
        level: 'warn',
        path,
        label,
        message: '这一项本该只写一行，多出的换行会被拼成一个值',
      });
    }
  }

  pruneEmptyItems(data, arrayKeys);

  return { data, issues };
}

/** 从栈里找出某个循环当前的下标。 */
function stackIndexFor(stack: ItemCtx[], loop: string): number {
  for (let i = stack.length - 1; i >= 0; i--) {
    if (stack[i].loop === loop) return stack[i].index;
  }
  return 0;
}

/**
 * 让条目对象在写子字段之前就存在。
 *
 * `loop` 是数组本身的路径（`漏洞列表` 或 `问题分组[0].条目`），
 * 所以要走的步数是「除最后一段以外」——最后一段是数组名，不是容器。
 * 早先这里把整条路径都当容器走了一遍，等于访问 `root.漏洞列表.漏洞列表`，
 * 顶层数组于是永远建不出来，所有子字段写进一个孤立的空对象里。
 */
function ensureItem(root: Record<string, unknown>, loop: string, index: number) {
  const segs = splitPath(loop);
  const last = segs.pop();
  if (!last) return;

  let cur: any = root;
  for (const { name, index: arrIdx } of segs) {
    if (arrIdx === undefined) {
      if (cur[name] == null || typeof cur[name] !== 'object') cur[name] = {};
      cur = cur[name];
    } else {
      if (!Array.isArray(cur[name])) cur[name] = [];
      const arr = cur[name] as any[];
      while (arr.length <= arrIdx) arr.push(null);
      if (arr[arrIdx] == null || typeof arr[arrIdx] !== 'object') arr[arrIdx] = {};
      cur = arr[arrIdx];
    }
  }

  if (!Array.isArray(cur[last.name])) cur[last.name] = [];
  const arr = cur[last.name] as any[];
  while (arr.length <= index) arr.push({});
  if (arr[index] == null || typeof arr[index] !== 'object') arr[index] = {};
}

/**
 * 图表 JSON 的容错解析。
 *
 * 用户手写 JSON 一定会写错（多一个逗号、少一个括号）。这里必须把错因说清楚，
 * 而不是抛一个 500 让他在「预览数据」里看到一句「渲染失败」。同时校验
 * **系列数不可增删**——这是渲染侧的硬约束：`c:ser` 的顺序决定了配色，
 * 多一个系列就落回默认色，用户说的「不要改样式」正是这个意思。
 */
function parseChartJSON(raw: string): { value?: unknown; error?: string } {
  let v: any;
  try {
    v = JSON.parse(raw);
  } catch (e: any) {
    return { error: `图表数据不是合法 JSON：${String(e?.message ?? e).slice(0, 80)}` };
  }
  if (!v || typeof v !== 'object') return { error: '图表数据应是一个对象' };
  if (!Array.isArray(v.categories)) return { error: '图表数据缺少 categories 数组' };
  if (!Array.isArray(v.series)) return { error: '图表数据缺少 series 数组' };
  for (const [i, s] of v.series.entries()) {
    if (!s || typeof s !== 'object') return { error: `第 ${i + 1} 个系列格式不对` };
    if (!Array.isArray(s.values)) return { error: `第 ${i + 1} 个系列缺少 values 数组` };
    if (s.values.some((x: unknown) => typeof x !== 'number' || Number.isNaN(x))) {
      return { error: `第 ${i + 1} 个系列的 values 里必须全是数字` };
    }
  }
  return { value: v };
}

/** 段落的 HTML（保留加粗/斜体等行内标记），供 richtext 槽位用。 */
function paragraphHTML(node: any): string {
  const inline = (n: any): string => {
    if (!n) return '';
    if (n.type === 'text') {
      let t = String(n.text ?? '');
      // 富文本最终进 docx，`<` `&` 必须转义，否则整份文档解析失败
      t = t.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
      for (const m of n.marks ?? []) {
        if (m.type === 'bold') t = `<strong>${t}</strong>`;
        else if (m.type === 'italic') t = `<em>${t}</em>`;
        else if (m.type === 'strike') t = `<s>${t}</s>`;
        else if (m.type === 'code') t = `<code>${t}</code>`;
      }
      return t;
    }
    if (n.type === 'hardBreak') return '<br>';
    if (Array.isArray(n.content)) return n.content.map(inline).join('');
    return '';
  };
  const body = (node.content ?? []).map(inline).join('');
  return body ? `<p>${body}</p>` : '<p></p>';
}

/**
 * 清掉全空的循环条目，并把后面的下标前移。
 *
 * 用户点了「加一条」却一个字没填时，不该让文档多出一行空表格。判定用
 * 「该条目下所有叶子是否全空」——占位产生的空对象和用户填了空字符串在
 * 结构上没区别，只能看内容。
 */
function pruneEmptyItems(data: Record<string, unknown>, keepArrayKeys: Set<string>) {
  for (const key of Object.keys(data)) {
    const arr = data[key];
    if (!Array.isArray(arr)) continue;
    const kept = arr.filter((x) => !isEmptyDeep(x));
    // 一条都不剩就把这个键也去掉。留一个 `漏洞列表: []` 会让「空表单」
    // 看起来像「填了一个空列表」，而两者在服务端校验里的含义不同。
    if (kept.length === 0) {
      // 原数据里本来就有这个数组（哪怕是空的），保留它；否则删掉
      if (keepArrayKeys.has(key)) data[key] = [];
      else delete data[key];
    } else data[key] = kept;
  }
}

function isEmptyDeep(v: unknown): boolean {
  if (v == null) return true;
  if (typeof v === 'string') return isBlankText(v);
  if (typeof v === 'number' || typeof v === 'boolean') return false;
  if (Array.isArray(v)) return v.every(isEmptyDeep);
  if (typeof v === 'object') {
    const o = v as Record<string, unknown>;
    // 图片对象只要有 src 就算有内容
    if (o.type === 'image' && typeof o.src === 'string' && o.src) return false;
    if (o.type === 'qrcode' && typeof o.text === 'string' && o.text) return false;
    return Object.values(o).every(isEmptyDeep);
  }
  return false;
}

export { getPath };
