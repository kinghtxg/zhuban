/**
 * 富文本的两个纯函数：**把旧 markdown 升成 html**、**把任意 html 收成白名单**。
 *
 * 为什么必须有白名单这一层：富文本最终会变成 Word 里的 run。用户在
 * contenteditable 里粘贴任何东西都会带进一堆 `<span class="MsoNormal">`、
 * `style="font-family:宋体"`、`<table>` 之类的东西。这些东西一旦进了
 * `{type:'html', content}`，就会被渲染服务当成"用户想要的格式"：
 *   - 它带来的 font-family 会覆盖模板的字体（模板里明明写着仿宋，成稿成了宋体）
 *   - 它带来的 font-size 会覆盖模板的字号
 * 而「格式一个字节都不许改」是硬要求。所以这里只放行**语义标签**：
 * 加粗 / 斜体 / 下划线 / 删除线 / 换行 / 颜色 / 图片。段落的居中、缩进、
 * 行距、编号一律由模板的段落属性决定，富文本不许染指。
 */

/** 允许保留的标签。全部是"文字语义"，没有一个能改变版面几何。 */
const KEEP = new Set(['B', 'STRONG', 'I', 'EM', 'U', 'S', 'STRIKE', 'BR', 'IMG', 'SPAN', 'FONT']);

/**
 * 连内容一起丢掉的标签。
 *
 * 必须是"丢整棵子树"，不能只剥标签：`<script>alert(1)</script>` 剥掉标签后
 * 里面的 `alert(1)` 会变成正文，而 `<style>p{color:red}</style>` 的规则文本
 * 会原样出现在报告里。这个坑踩过一次——看似白名单生效了，实际把那句话印了出来。
 */
const DROP = new Set([
  'SCRIPT', 'STYLE', 'LINK', 'META', 'TITLE', 'HEAD', 'IFRAME', 'OBJECT',
  'EMBED', 'NOSCRIPT', 'SVG', 'MATH', 'TEMPLATE', 'HEAD',
]);

/** 只允许这几种图片地址。javascript:/file:/blob: 一律丢。 */
function safeSrc(src: string): string {
  const s = (src || '').trim();
  if (/^data:image\//i.test(s)) return s;
  if (/^https?:\/\//i.test(s)) return s;
  return '';
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

/**
 * 旧 markdown → html 的**最小**转换。
 *
 * 存在的唯一理由：3.4.16 之前富文本字段默认存 `{type:'markdown'}`，界面上的
 * 输入框提示写的是「支持 **加粗** 等简单格式」。用户照着写了 `**重点**`。
 * 现在换成交互式编辑器，若不转换，那些星号会**原样**出现在正式报告里——
 * 用户的文字被我们悄悄改了，这比不给他加粗严重得多。
 *
 * 所以只转当初承诺过的那几种：加粗、斜体、删除线、换行、`- ` 列表。
 * 其它 markdown 语法（表格、引用、代码块）本来就没承诺过，保持字面，
 * 至少不会改写原意。
 */
export function mdToHtmlLite(src: string): string {
  let s = escapeHtml(src || '');
  s = s.replace(/\*\*([^*\n]+)\*\*/g, '<b>$1</b>');
  s = s.replace(/(^|[^*])\*([^*\n]+)\*/g, '$1<i>$2</i>');
  s = s.replace(/(^|[^_])_([^_\n]+)_/g, '$1<i>$2</i>');
  s = s.replace(/~~([^~\n]+)~~/g, '<s>$1</s>');
  s = s
    .split(/\n/)
    .map((line) => (/^\s*[-*]\s+/.test(line) ? '• ' + line.replace(/^\s*[-*]\s+/, '') : line))
    .join('<br/>');
  return s;
}

/** 把 DOM 节点收成白名单 html 字符串。 */
function serialize(node: Node, out: string[]): void {
  if (node.nodeType === Node.TEXT_NODE) {
    out.push(escapeHtml(node.nodeValue || ''));
    return;
  }
  if (node.nodeType !== Node.ELEMENT_NODE) return;
  const el = node as HTMLElement;
  const tag = el.tagName.toUpperCase();

  if (DROP.has(tag)) return;

  if (tag === 'BR') {
    out.push('<br/>');
    return;
  }
  if (tag === 'IMG') {
    const src = safeSrc(el.getAttribute('src') || '');
    if (src) out.push(`<img src="${src.replace(/"/g, '&quot;')}"/>`);
    return;
  }

  const keep = KEEP.has(tag);
  let open = '';
  let close = '';
  if (keep) {
    if (tag === 'SPAN' || tag === 'FONT') {
      // 只留颜色：颜色不改版面，是"内容的一部分"（标红的风险点）。
      // 字体、字号、字距一概不放行——那是模板的事。
      const color = colorOf(el);
      if (color) {
        open = `<span style="color:${color}">`;
        close = '</span>';
      }
    } else {
      open = `<${tag.toLowerCase()}>`;
      close = `</${tag.toLowerCase()}>`;
    }
  }
  out.push(open);
  el.childNodes.forEach((c) => serialize(c, out));
  out.push(close);
}

const NAMED: Record<string, string> = {
  red: '#ff0000', blue: '#0000ff', green: '#008000', black: '#000000',
  white: '#ffffff', gray: '#808080', grey: '#808080', orange: '#ffa500',
  purple: '#800080', brown: '#a52a2a', navy: '#000080',
};

function colorOf(el: HTMLElement): string {
  const raw = (el.style?.color || el.getAttribute('color') || '').trim().toLowerCase();
  if (!raw) return '';
  if (/^#[0-9a-f]{3}$/.test(raw) || /^#[0-9a-f]{6}$/.test(raw)) return raw;
  if (/^rgb\(\s*\d{1,3}\s*,\s*\d{1,3}\s*,\s*\d{1,3}\s*\)$/.test(raw)) return raw;
  return NAMED[raw] || '';
}

/**
 * 任意 html → 白名单 html。
 *
 * 用 DOMParser 而不是正则：正则处理不了 `<span class="a">` 与 `<img src=x onerror=y>`；
 * 而 `onerror` 这类属性只要漏一个，拖进编辑器就等于一个 XSS。
 * DOMParser 在惰性文档里解析，脚本和事件处理器都不会执行。
 */
export function sanitizeRichHtml(html: string): string {
  if (!html) return '';
  const doc = new DOMParser().parseFromString(html, 'text/html');
  const out: string[] = [];
  serialize(doc.body, out);
  return out.join('');
}

/** 白名单 html 里有几行字（用于"空不空"的判断与摘要）。 */
export function richHtmlText(html: string): string {
  if (!html) return '';
  const doc = new DOMParser().parseFromString(html, 'text/html');
  // <br/> 变真实换行，否则"第1项第2项"会连成一串，摘要读不出结构
  doc.body.querySelectorAll('br').forEach((br) => br.replaceWith('\n'));
  return (doc.body.textContent || '').replace(/\u00a0/g, ' ');
}

/** 富文本里有没有图。 */
export function richHtmlHasImage(html: string): boolean {
  return /<img\s/i.test(html || '');
}
