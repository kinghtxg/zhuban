/**
 * 骨架 + 数据 → 编辑器文档（ProseMirror JSON）。
 *
 * 和 extract.ts 互为逆运算。两者必须严格互逆，否则用户「打开 → 什么都不改 →
 * 提交」就会产出一份和原数据不同的文档 —— 那正是「格式变化」最容易发生的地方，
 * 而且用户完全没有察觉。mdfill.test.ts 里的往返用例就是守这一条。
 *
 * 文档是扁平的：每个骨架标题一个 `fillHeading` 原子节点，其后跟内容节点。
 * 见 extract.ts 开头对「为什么不做嵌套」的说明。
 */

import type { SkeletonChart, SkeletonHeading, SkeletonNode, SlotKind } from './skeleton';
import type { ChartPayload } from '@/components/ChartDataDrawer';

export interface PMNode {
  type: string;
  attrs?: Record<string, unknown>;
  content?: PMNode[];
  text?: string;
  marks?: { type: string }[];
}

/** 标题节点的属性集。提取端按同一套字段读回，改一处必须改两处。 */
function headingAttrs(n: SkeletonHeading): Record<string, unknown> {
  return {
    path: n.path,
    label: n.label,
    level: n.level,
    slot: n.slot,
    hint: n.hint ?? null,
    required: !!n.required,
    enum: n.enum ?? null,
    paraField: n.paraField ?? null,
    imageField: n.imageField ?? null,
    itemPath: n.itemPath ?? null,
    itemIndex: n.itemIndex ?? null,
  };
}

function textNode(text: string): PMNode {
  return { type: 'text', text };
}

/**
 * 文本 → 段落内容。
 *
 * 换行转 hardBreak 而不是拆成多段：一个标量字段在数据里就是一个字符串，
 * 拆成多段再拼回来虽然值一样，但界面上会变成两段独立正文，用户看不出
 * 「这其实是一个字段」——他会以为是两段各自独立的文字，然后奇怪为什么
 * 删掉第二段第一段也跟着变。
 */
function inline(text: string): PMNode[] {
  const parts = text.split('\n');
  const out: PMNode[] = [];
  parts.forEach((p, i) => {
    if (i > 0) out.push({ type: 'hardBreak' });
    if (p) out.push(textNode(p));
  });
  return out;
}

function paragraph(text: string): PMNode {
  const content = inline(text);
  return content.length ? { type: 'paragraph', content } : { type: 'paragraph' };
}

function codeBlock(text: string): PMNode {
  return text
    ? { type: 'codeBlock', attrs: { language: 'json' }, content: [textNode(text)] }
    : { type: 'codeBlock', attrs: { language: 'json' } };
}

function imageNode(src: string): PMNode {
  return { type: 'image', attrs: { src, alt: null, title: null } };
}

/** 从值里取图片 src。兼容字符串、`{src}`、`{type:'image',src}` 三种历史形态。 */
function srcOf(v: unknown): string {
  if (typeof v === 'string') return v;
  if (v && typeof v === 'object') {
    const s = (v as Record<string, unknown>).src;
    if (typeof s === 'string') return s;
  }
  return '';
}

function asText(v: unknown): string {
  if (v == null) return '';
  if (typeof v === 'string') return v;
  if (typeof v === 'number' || typeof v === 'boolean') return String(v);
  return '';
}

/**
 * 极简 HTML → 段落。只认我们自己产出的那套标签
 * （`<p>` 与 `<strong>/<em>/<s>/<code>/<br>`），不引入完整解析器。
 *
 * 为什么不引：这个 HTML 的来源只有两处 —— 上一次保存的 richtext 值，
 * 和 extract.ts 的 paragraphHTML。两边都只产出这几个标签。为它装一个
 * 解析库，等于给整个填写页加几十 KB 去处理永远不会出现的输入。
 */
function htmlToParagraphs(html: string): PMNode[] {
  const out: PMNode[] = [];
  const paraRe = /<p>([\s\S]*?)<\/p>/gi;
  let m: RegExpExecArray | null;
  let matched = false;
  while ((m = paraRe.exec(html)) !== null) {
    matched = true;
    out.push({ type: 'paragraph', content: parseInline(m[1]) });
  }
  if (!matched) {
    // 没有 <p> 包裹时（用户手写了裸文本），整体当一个段落
    const t = html.replace(/<[^>]+>/g, '').trim();
    if (t) out.push(paragraph(decodeEntities(t)));
  }
  return out;
}

function parseInline(src: string): PMNode[] {
  const out: PMNode[] = [];
  let rest = src;
  const tagRe = /<(strong|em|s|code)>([\s\S]*?)<\/\1>|<br\s*\/?>/gi;
  let m: RegExpExecArray | null;
  while ((m = tagRe.exec(rest)) !== null) {
    // 标签之前的裸文本
    if (m.index > 0) {
      const before = rest.slice(0, m.index);
      for (const n of plainInline(before)) out.push(n);
    }
    if (m[0].toLowerCase().startsWith('<br')) {
      out.push({ type: 'hardBreak' });
    } else {
      const markType = m[1] === 'strong' ? 'bold' : m[1] === 'em' ? 'italic' : m[1] === 's' ? 'strike' : 'code';
      const inner = decodeEntities(m[2].replace(/<[^>]+>/g, ''));
      if (inner) {
        out.push({ type: 'text', text: inner, marks: [{ type: markType }] });
      }
    }
    rest = rest.slice(m.index + m[0].length);
    tagRe.lastIndex = 0;
  }
  if (rest) for (const n of plainInline(rest)) out.push(n);
  return out;
}

function plainInline(s: string): PMNode[] {
  const t = decodeEntities(s.replace(/<[^>]+>/g, ''));
  return t ? [{ type: 'text', text: t }] : [];
}

function decodeEntities(s: string): string {
  return s
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&#39;/g, "'")
    // `&amp;` 必须最后替换：先换它会把 `&amp;lt;` 变成 `&lt;` 再变成 `<`，
    // 而用户原本要的就是字面量 `&lt;`。
    .replace(/&amp;/g, '&');
}

function chartPayloadOf(n: SkeletonChart, data?: Record<string, unknown>): ChartPayload {
  const v = data ? readPath(data, n.path) : undefined;
  if (v && typeof v === 'object' && Array.isArray((v as ChartPayload).categories)) {
    const p = v as ChartPayload;
    return {
      categories: p.categories.map(String),
      series: (p.series || []).map((s) => ({
        name: String(s?.name ?? ''),
        values: (s?.values || []).map((x) => Number(x) || 0),
      })),
    };
  }
  return n.payload;
}

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

/** 一个标题下要摆的内容节点。 */
function contentOf(n: SkeletonHeading, data?: Record<string, unknown>): PMNode[] {
  const v = data ? readPath(data, n.path) : undefined;

  switch (n.slot) {
    case 'item':
      return [];

    case 'paragraphs': {
      const field = n.paraField || '段落';
      const arr = Array.isArray(v) ? v : [];
      if (arr.length === 0) return [paragraph('')];
      return arr.map((item: any) => {
        const raw = item && typeof item === 'object' ? item[field] : item;
        return paragraph(asText(raw));
      });
    }

    case 'richtext': {
      const content = (v as any)?.content;
      if (typeof content === 'string' && content.trim()) {
        const ps = htmlToParagraphs(content);
        return ps.length ? ps : [paragraph('')];
      }
      return [paragraph('')];
    }

    case 'image': {
      const src = srcOf(v);
      return src ? [imageNode(src)] : [];
    }

    case 'images': {
      const arr = Array.isArray(v) ? v : [];
      const field = n.imageField || 'image';
      return arr
        .map((item: any) => srcOf(item && typeof item === 'object' ? item[field] : item))
        .filter(Boolean)
        .map(imageNode);
    }

    case 'qrcode': {
      const t = asText((v as any)?.text);
      return [paragraph(t)];
    }

    default: {
      // 标量：空值也留一个空段落当输入位，否则用户点不进这个字段
      return [paragraph(asText(v))];
    }
  }
}

/**
 * 主函数。
 *
 * `data` 为空时得到一份「空表单」——每个字段一个空段落，用户从头写。
 * 传了 `data` 则灌进去，用于「打开已有草稿」和「从示例数据预填」。
 */
export function toDocJSON(nodes: SkeletonNode[], data?: Record<string, unknown>): { type: 'doc'; content: PMNode[] } {
  const content: PMNode[] = [];

  for (const n of nodes) {
    if (n.kind === 'chart') {
      content.push({
        type: 'fillHeading',
        attrs: {
          path: n.path,
          label: n.label,
          level: 2,
          slot: 'chart',
          hint: `${n.seriesCount} 个系列 · ${n.pointCount} 个分类`,
          required: false,
          chartKind: n.chartKind,
          chartTitle: n.title,
          seriesCount: n.seriesCount,
          pointCount: n.pointCount,
        },
      });
      content.push(codeBlock(JSON.stringify(chartPayloadOf(n, data), null, 2)));
      continue;
    }

    content.push({ type: 'fillHeading', attrs: headingAttrs(n) });
    content.push(...contentOf(n, data));
  }

  // 文档不能为空：Tiptap 的空文档也要一个段落兜底，否则光标无处落脚
  if (content.length === 0) content.push({ type: 'paragraph' });
  return { type: 'doc', content };
}

export type { SlotKind };
