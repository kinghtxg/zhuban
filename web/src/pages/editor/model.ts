// 编辑器模型纯函数：与 render-service docmodel/docapply 对齐的序列化与 id 规则。
import type { EditBlock, EditToken } from '@/types/editor';

// ---------------------------------------------------------------------------
// id 规则（docmodel.parse 同名规则，两侧必须一致）：
//   顶层段落 p0,p1... 表格 t0,t1...；行 t0r0...；单元格 t0r0c0...；单元格段落 t0r0c0p0...
// 前端新增块一律 new- 前缀，由 apply 按相邻锚点插入。
// ---------------------------------------------------------------------------

let newSeq = 0;
export function nextNewId(prefix = 'new'): string {
  newSeq += 1;
  return `${prefix}-p${Date.now().toString(36)}${newSeq}`;
}

// 从 "p3" 抽序号；无法解析返回 null
function paraIndex(id: string): number | null {
  const m = /^p(\d+)$/.exec(id);
  return m ? parseInt(m[1], 10) : null;
}

/**
 * 把前端块数组序列化为 apply 真正接受的形状。
 * 关键：docapply 不认识 list 块（会把聚合段落坍缩），这里把 list 块展开为
 * 逐段 paragraph 块（id = p{suffix+i}），保证列表样式与文字在回写时原样保留。
 * 其余块透传；new- 前缀块由 apply 插入。
 */
export function serializeForApply(blocks: EditBlock[]): Record<string, unknown>[] {
  const out: Record<string, unknown>[] = [];
  for (const b of blocks) {
    if (b.type === 'list') {
      const idx = paraIndex(b.id);
      if (idx === null) continue;
      b.items.forEach((it, i) => {
        out.push({ id: `p${idx + i}`, type: 'paragraph', text: it.text ?? '' });
      });
      continue;
    }
    if (b.type === 'table') {
      out.push({
        id: b.id,
        type: 'table',
        rows: b.rows.map((r) => ({
          id: r.id,
          loop_row: !!r.loop_row,
          loop: r.loop_row ? r.loop : undefined,
          cells: r.cells.map((c) => ({
            blocks: c.blocks.map((cb) => ({
              id: cb.id,
              type: cb.type,
              ...(cb.type === 'paragraph' ? { text: cb.text ?? '' } : {}),
              ...(cb.type === 'placeholder'
                ? {
                    kind: cb.kind,
                    name: cb.name,
                    width_mm: cb.width_mm,
                    height_mm: cb.height_mm,
                    asset_id: cb.asset_id,
                    signature_id: cb.signature_id,
                    seal_id: cb.seal_id,
                  }
                : {}),
              ...(cb.type === 'unsupported' ? { note: cb.note } : {}),
            })),
          })),
        })),
      });
      continue;
    }
    if (b.type === 'placeholder') {
      out.push({
        id: b.id,
        type: 'placeholder',
        kind: b.kind,
        name: b.name,
        width_mm: b.width_mm,
        height_mm: b.height_mm,
        asset_id: b.asset_id,
        signature_id: b.signature_id,
        seal_id: b.seal_id,
      });
      continue;
    }
    if (b.type === 'unsupported') {
      out.push({ id: b.id, type: 'unsupported', note: b.note });
      continue;
    }
    // 图表块不投向 apply：它的段落里挂的是 <w:drawing>，任何文本回写都会把图删掉。
    // 后端 docapply 也会把 chart 类型直接 return，这里先挡一道，避免"靠 id 不在
    // 映射表里"这种偶然的安全。
    if (b.type === 'chart') {
      continue;
    }
    // heading / paragraph
    out.push({ id: b.id, type: b.type, ...(b.type === 'heading' ? { level: b.level } : {}), text: b.text ?? '' });
  }
  return out;
}

// ---------------------------------------------------------------------------
// token 高亮：把 "本报告依据 {{测试人员}} 出具" 切成片段
// ---------------------------------------------------------------------------
export interface TextSeg {
  text: string;
  kind: 'plain' | EditToken['kind'];
}
export function segmentTokens(text: string, tokens?: EditToken[]): TextSeg[] {
  if (!tokens || tokens.length === 0) return [{ text, kind: 'plain' }];
  const segs: TextSeg[] = [];
  let cursor = 0;
  for (const t of tokens) {
    if (t.start > cursor) segs.push({ text: text.slice(cursor, t.start), kind: 'plain' });
    segs.push({ text: t.text, kind: t.kind });
    cursor = t.end;
  }
  if (cursor < text.length) segs.push({ text: text.slice(cursor), kind: 'plain' });
  return segs;
}

// ---------------------------------------------------------------------------
// 草稿（localStorage）：按 template+version 维度
// ---------------------------------------------------------------------------
const draftKey = (templateId: string, versionId: string) =>
  `docforge:editor:draft:${templateId}:${versionId}`;

export interface DraftPayload {
  blocks: EditBlock[];
  savedAt: number;
}

export function loadDraft(templateId: string, versionId: string): DraftPayload | null {
  try {
    const raw = localStorage.getItem(draftKey(templateId, versionId));
    if (!raw) return null;
    const p = JSON.parse(raw) as DraftPayload;
    if (!p || !Array.isArray(p.blocks)) return null;
    return p;
  } catch {
    return null;
  }
}
export function writeDraft(templateId: string, versionId: string, blocks: EditBlock[]) {
  try {
    localStorage.setItem(
      draftKey(templateId, versionId),
      JSON.stringify({ blocks, savedAt: Date.now() } satisfies DraftPayload),
    );
  } catch {
    /* 存储满或被禁用时静默 */
  }
}
export function clearDraft(templateId: string, versionId: string) {
  try {
    localStorage.removeItem(draftKey(templateId, versionId));
  } catch {
    /* ignore */
  }
}

// 占位符变量名前缀约定（与 scanner._kind_by_name 同口径）
export function kindName(kind: string, base: string): string {
  const b = base.trim();
  if (kind === 'image') return b.startsWith('image_') || b.startsWith('img_') ? b : `image_${b || 'logo'}`;
  if (kind === 'qrcode') return b.startsWith('qr_') ? b : `qr_${b || 'code'}`;
  if (kind === 'rich')
    return b.startsWith('rich_') || b.startsWith('html_') || b.startsWith('md_') ? b : `rich_${b || 'text'}`;
  return b;
}
