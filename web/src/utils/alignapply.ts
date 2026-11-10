/**
 * 把「对齐建议」写进表单数据。
 *
 * 这一步是纯函数，刻意与界面解耦：建议对不对可以争论，但"写进去的位置对不对"
 * 必须是可判定的——写错一格比不写更糟，用户不会逐条核，他会直接生成，
 * 然后拿到一份「影响范围」里写着公司名的报告。
 *
 * 三个必须处理的现实问题：
 *
 * 1. **路径形状不同**。模板里的 `{{ 漏洞.名称 }}` 在 schema 里是
 *    `漏洞列表[].名称`。直接 `values[path] = x` 会造出一个叫
 *    `漏洞列表[].名称` 的顶层键——表单看不见它，接口却会把这份垃圾一起收下。
 * 2. **值形状不同**。图片字段要 `{type:'image',src}`，富文本要
 *    `{type:'html',content}`。写进一个裸字符串的话，`/render` 会把它
 *    当成**文字**渲染出来——报告里出现一条长得像 base64 的乱码。
 * 3. **有些字段绝不能碰**。签章（sig_/seal_）是授权资源，不是文字；
 *    条件段是布尔开关；图表有自己的数据网格。对齐结果里万一带了这些，
 *    一律跳过，而不是"尽力而为"地改一下。
 */
import type { AlignMatch, TemplateVariable } from '@/types/api';
import { blankItem, isSignatureVar } from '@/components/FillForm';

type Values = Record<string, unknown>;

export interface ApplyOutcome {
  next: Values;
  /** 真正写进去的字段（显示给人看"改了哪些"） */
  applied: string[];
  /** 跳过的，带原因。空着不解释等于骗人 */
  skipped: { slot: string; reason: string }[];
}

function asObject(v: unknown): Values {
  return v && typeof v === 'object' && !Array.isArray(v) ? (v as Values) : {};
}

/** 把 html 里的裸文字转义后套成 html（引擎给的通常已经是 html，走这里的是兜底）。 */
function toHtmlValue(raw: string): { type: 'html'; content: string } {
  const esc = raw
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/\n/g, '<br/>');
  return { type: 'html', content: esc };
}

/**
 * 按字段类型把值收成 schema 认的形状。
 *
 * 返回 null 表示"这个值给这个字段没有意义"，调用方按跳过处理。
 */
export function coerceForVar(v: TemplateVariable, value: unknown): unknown | null {
  if (v.kind === 'chart' || v.kind === 'condition') return null;
  if (v.kind === 'image') {
    if (isSignatureVar(v)) return null;
    const src =
      typeof value === 'string' ? value : String(asObject(value).src || '');
    // 只认能取到的地址。空字符串写进去会让 /validate 判「应为对象」
    if (!src.trim()) return null;
    return { type: 'image', src };
  }
  if (v.kind === 'qrcode') {
    const text = typeof value === 'string' ? value : String(asObject(value).text || '');
    if (!text.trim()) return null;
    return { type: 'qrcode', text, size: 20 };
  }
  if (v.kind === 'richtext') {
    const obj = asObject(value);
    if (typeof obj.content === 'string' && (obj.type === 'html' || obj.type === 'markdown')) {
      return obj.content.trim() ? obj : null;
    }
    if (typeof value === 'string') return value.trim() ? toHtmlValue(value) : null;
    return null;
  }
  // 标量 / 枚举：一律按字符串。数字类字段传成字符串，渲染时也是当文字用，
  // 而传成 number 反而可能被 schema 拒（扫描器把它标成了 string）。
  const s = typeof value === 'string' ? value : value == null ? '' : String(value);
  return s.trim() ? s : null;
}

/** 在变量树里按 schema 路径找到目标变量。 */
export function findVar(vars: TemplateVariable[], path: string): TemplateVariable | null {
  // 先按"整个路径就是一个变量名"查：docx 里的 `{{ 详情段.段落 }}` 扫描出来
  // 的变量名本身就是带点的 `详情段.段落`。不先查这一下，会被下面按点拆分的
  // 逻辑拆成「详情段 / 段落」，然后报「模板里没有这个字段」——明明有。
  const exact = vars.find((v) => v.name === path);
  if (exact) return exact;

  const m = path.match(/^([^[.\]]+)(?:\[\])?\.(.+)$/);
  if (!m) return null;
  const root = vars.find((v) => v.name === m[1]);
  if (!root) return null;
  // 每个片段都要剥掉自己的 `[]`：`漏洞列表[].建议[].段落` 拆出来是
  // 「建议[] / 段落」，带着方括号去比 name 永远比不到 —— 而变量树里
  // 那两层明明都存在，报的却是"模板里没有这个字段"。
  const rest = m[2]
    .split('.')
    .map((s) => (s.endsWith('[]') ? s.slice(0, -2) : s))
    .filter(Boolean);
  let cur: TemplateVariable | undefined = root;
  for (const seg of rest) {
    cur = (cur?.fields || []).find((f) => f.name === seg);
    if (!cur) return null;
  }
  return cur || null;
}

/** 一项明细是不是还空着（不能误判成"空"而把用户填好的内容盖掉）。 */
function isEmptyItem(it: Values | undefined, fields: TemplateVariable[]): boolean {
  if (!it) return true;
  return fields.every((f) => {
    const v = it[f.name];
    if (Array.isArray(v)) return v.length === 0;
    if (v && typeof v === 'object') {
      const o = v as Record<string, unknown>;
      const inner = o.content ?? o.text ?? o.src;
      return !String(inner ?? '').trim();
    }
    return v === undefined || v === null || String(v).trim() === '';
  });
}

/** 这一项里的某个字段是不是已经有内容了。 */
function fieldFilled(item: Values | undefined, name: string): boolean {
  if (!item) return false;
  const v = item[name];
  if (Array.isArray(v)) return v.length > 0;
  if (v && typeof v === 'object') {
    const o = v as Record<string, unknown>;
    return Boolean(String(o.content ?? o.text ?? o.src ?? '').trim());
  }
  return v !== undefined && v !== null && String(v).trim() !== '';
}

interface Seg {
  name: string;
  isArray: boolean;
}

/** `漏洞列表[].建议[].段落` → [{漏洞列表,true},{建议,true},{段落,false}] */
function parsePath(path: string): Seg[] {
  return path
    .split('.')
    .filter(Boolean)
    .map((s) => ({
      name: s.endsWith('[]') ? s.slice(0, -2) : s,
      isArray: s.endsWith('[]'),
    }));
}

/**
 * 写入一个 schema 路径。支持 `A[].f` 与 `A[].B[].f` 两种形状。
 *
 * ## 为什么要有"游标"
 *
 * 一条漏洞的「名称 / 影响范围 / 漏洞地址」必须落在**同一行明细**里。
 * 早先每个字段各自去找"第一个还空着的项"：名称写进第 1 项之后第 1 项就不空了，
 * 影响范围于是写进第 2 项——一份报告里同一条漏洞被拆成两条，第一条只有名字、
 * 第二条只有影响范围。结果是"填进去了"却完全不能用，而界面还报成功。
 * 所以同一批写入要沿着同一个游标往下走，只有"字段重复出现"才另起一行。
 *
 * 绝不覆盖已经填好的行：用户可能先手填了第 1 项再来粘整篇，
 * 覆盖掉等于用识别结果抹掉他已经确认过的内容。
 */
export function writePath(
  values: Values,
  vars: TemplateVariable[],
  path: string,
  value: unknown,
  cursors: Map<string, number> = new Map(),
): Values | null {
  const segs = parsePath(path);
  if (!segs.length) return null;

  const root: Values = { ...values };
  let container: Values = root;
  let level: TemplateVariable[] = vars;
  let key = '';

  for (let i = 0; i < segs.length; i++) {
    const seg = segs[i];
    const last = i === segs.length - 1;
    const def = level.find((v) => v.name === seg.name);
    if (!def) return null;

    if (!seg.isArray) {
      if (last) {
        container[seg.name] = value;
        return root;
      }
      // 普通对象层：schema 里少见，但保持正确
      container[seg.name] = { ...asObject(container[seg.name]) };
      container = container[seg.name] as Values;
      level = def.fields || [];
      continue;
    }

    const fields = def.fields || [];
    const arr: Values[] = Array.isArray(container[seg.name])
      ? (container[seg.name] as Values[]).slice()
      : [];
    container[seg.name] = arr;

    const ck = `${key}/${seg.name}`;
    let idx = cursors.get(ck);
    if (idx === undefined) {
      idx = arr.findIndex((it) => isEmptyItem(it, fields));
      if (idx < 0) {
        arr.push(blankItem(fields));
        idx = arr.length - 1;
      }
    }
    while (arr.length <= idx) arr.push(blankItem(fields));

    // 下一段就是这个数组里的字段，而当前项已经有它了 —— 说明又来了一条
    // 同类记录（再粘一次整篇，或多段描述）。另起一项，别覆盖。
    const fieldName = !last && i + 1 === segs.length - 1 ? segs[i + 1].name : '';
    if (fieldName && fieldFilled(arr[idx], fieldName)) {
      arr.push(blankItem(fields));
      idx = arr.length - 1;
    }
    cursors.set(ck, idx);

    container = arr[idx] as Values;
    level = fields;
    key = `${ck}#${idx}`;
  }
  return null;
}

/** 把选中的建议逐条写进表单数据。任何一条写不进去都要说清原因。 */
export function applyAlignMatches(
  values: Values,
  vars: TemplateVariable[],
  matches: AlignMatch[],
): ApplyOutcome {
  let next: Values = { ...values };
  const applied: string[] = [];
  const skipped: { slot: string; reason: string }[] = [];
  // 游标要**跨条**共享：同一条漏洞的各个字段必须落进同一行明细。
  const cursors = new Map<string, number>();

  for (const mt of matches) {
    const path = (mt.slot_path || mt.slot || '').trim();
    if (!path) {
      skipped.push({ slot: mt.slot, reason: '这条建议没有对应的字段' });
      continue;
    }
    const v = findVar(vars, path);
    if (!v) {
      skipped.push({ slot: mt.slot, reason: '模板里没有这个字段（可能是表格列或统计值）' });
      continue;
    }
    if (v.system_resolved) {
      skipped.push({ slot: mt.slot, reason: '签章由服务端注入，不能自动填' });
      continue;
    }
    const coerced = coerceForVar(v, mt.value);
    if (coerced === null) {
      skipped.push({ slot: mt.slot, reason: '内容是空的，或者类型对不上' });
      continue;
    }
    const wrote = writePath(next, vars, path, coerced, cursors);
    if (!wrote) {
      skipped.push({ slot: mt.slot, reason: '这个位置需要多行明细，请手动填' });
      continue;
    }
    next = wrote;
    applied.push(v.name);
  }
  return { next, applied, skipped };
}
