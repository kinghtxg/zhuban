import { useEffect, useRef, useState } from 'react';
import type { CSSProperties } from 'react';
import { EditBlock, EditCellBlock, EditRow } from '@/types/editor';
import { segmentTokens } from './model';
import { lookupSealName } from './signsealApi';
import { baseFontPt, fitBox, mmToPx, paraCss, ptToPx, runCss, tableWidthMm } from './layout';

interface Props {
  block: EditBlock;
  selected: boolean;
  onSelect: () => void;
  onUpdateText: (text: string) => void;
  onCellText: (tableId: string, rowIdx: number, cellIdx: number, text: string) => void;
  onDelete: () => void;
  onAddRow: (tableId: string, rowIdx: number, where: 'above' | 'below') => void;
  onRemoveRow: (tableId: string, rowIdx: number) => void;
  onSetLoopRow: (tableId: string, rowIdx: number, loopRow: boolean, loop?: { var: string; iterable: string }) => void;
  /** 点中某个单元格：既用于进入编辑，也作为「插入签名/印章」的落点。 */
  onCellFocus?: (tableId: string, rowIdx: number, cellIdx: number) => void;
  /** 当前被点中的单元格（用于显示选中框） */
  focusedCell?: { tableId: string; rowIdx: number; cellIdx: number } | null;
}

// 段级标记（{%p for/if/endfor/endif %}）判定
function isMarker(text: string): boolean {
  return /^\s*\{%p\s/.test(text) && /%\}\s*$/.test(text);
}

// run 文本里的 jinja 标签就地高亮。
// 不复用 segmentTokens 的全局 offset：那是按整段文字算的，切到 run 之后
// 每个 run 只有自己那一段，跨 run 的标签会把下标算飞。
const INLINE_TAG_RE = /(\{\{[\s\S]*?\}\}|\{%[\s\S]*?%\}|\{#[\s\S]*?#\})/;
function RunText({ text }: { text: string }) {
  const parts = text.split(INLINE_TAG_RE);
  return (
    <>
      {parts.map((p, i) => {
        if (!p) return null;
        if (p.startsWith('{{')) return <span key={i} className="tok-var">{p}</span>;
        if (p.startsWith('{%')) return <span key={i} className="tok-block">{p}</span>;
        if (p.startsWith('{#')) return <span key={i} className="tok-comment">{p}</span>;
        return <span key={i}>{p}</span>;
      })}
    </>
  );
}

export default function BlockView(p: Props) {
  const { block } = p;

  if (block.type === 'chart') {
    // 图表段落本身不可在线改样式，但数据可以走变量替换。画布上把它画成一个
    // 明确的"图表卡片"，避免用户把它当成空段落想往里打字（那会删掉 drawing）。
    const seriesText = block.series
      .map((s) => s.name || '系列')
      .slice(0, 3)
      .join(' / ');
    return (
      <div className="df-block chart" onClick={p.onSelect} title="图表数据可通过变量传参替换，样式保持不变">
        <div className="df-chart-head">
          <span className="df-badge chart">{block.kind_name || '图表'}</span>
          <span className="df-chart-title">{block.title || '（无标题）'}</span>
        </div>
        <div className="df-chart-meta">
          {block.series_count} 个系列 · {block.point_count} 个数据点
          {seriesText ? ` · ${seriesText}` : ''}
        </div>
        {block.categories.length > 0 && (
          <div className="df-chart-axis">
            {block.categories.slice(0, 8).map((c, i) => (
              <span key={i} className="df-chart-tick">
                {c}
              </span>
            ))}
            {block.categories.length > 8 ? <span className="df-chart-tick">…</span> : null}
          </div>
        )}
        {block.variable ? (
          <div className="df-chart-var">
            数据变量 <code>{block.variable}</code>
          </div>
        ) : null}
      </div>
    );
  }

  if (block.type === 'unsupported') {
    // 「不支持」只表示"这一段改不了"，不是"文档坏了"。旧文案「需在 Word 处理」
    // 只给一个空白方块，用户既看不到盒子里有什么，也不知道到底该去 Word 干什么。
    // 现在把盒内原文一起显示出来（后端 parse 时提取），并说明生成时原样保留。
    const preview = (block.text ?? '').trim();
    return (
      <div
        className="df-block unsupported"
        onClick={p.onSelect}
        title={block.note || '该元素不参与在线排版，随文档原样保留'}
      >
        <span className="df-badge warn">原样保留</span>
        {preview ? (
          <span className="df-unsupported-text">{preview}</span>
        ) : (
          <span className="df-unsupported-text empty">（此处无可提取文字，图形/图片随文档保留）</span>
        )}
        <div className="df-unsupported-note">{block.note || '该元素不参与在线排版'}</div>
      </div>
    );
  }

  if (block.type === 'placeholder') {
    const isSig = !!block.signature_id;
    const isSeal = !!block.seal_id;
    // 独立成段的图片型占位：按记录的尺寸画，但一定要夹在版心内。
    // 老实现给的是一个固定 24px 高的胶囊，用户看不出"这里将来会是一枚多大的章"，
    // 也就无从判断它会不会把版面撑开。
    const isImage = block.kind === 'image' || block.kind === 'qrcode';
    const box = fitBox(block.width_mm, block.height_mm, 60, 40);
    const label = sealLabel(block, isSig, isSeal);
    return (
      <div
        className={`df-block ${p.selected ? 'selected' : ''}`}
        onClick={p.onSelect}
        style={paraCss(block.fmt)}
      >
        <span className="df-badge">{sealBadge(block.kind, isSig, isSeal)}</span>
        {isSig || isSeal ? (
          <SealThumb
            src={`/api/v1/${isSig ? 'signatures' : 'seals'}/${isSig ? block.signature_id : block.seal_id}/image`}
            label={label}
            wPx={mmToPx(box.w)}
            hPx={mmToPx(box.h)}
          />
        ) : (
          <span
            className="df-block-placeholder"
            style={isImage ? { width: mmToPx(box.w), height: mmToPx(box.h) } : undefined}
          >
            {label}
          </span>
        )}
      </div>
    );
  }

  if (block.type === 'list') {
    return (
      <div className={`df-block ${p.selected ? 'selected' : ''}`} onClick={p.onSelect}>
        <span className="df-badge">列表</span>
        <ul className="df-list">
          {block.items.map((it, i) => (
            <li key={i} style={paraCss(it.fmt)}>
              {it.runs && it.runs.length > 0 ? (
                it.runs.map((r, ri) => (
                  <span key={ri} style={runCss(r)}>
                    <RunText text={r.text} />
                  </span>
                ))
              ) : (
                <RunText text={it.text || '…'} />
              )}
            </li>
          ))}
        </ul>
      </div>
    );
  }

  if (block.type === 'table') {
    return (
      <TableView
        block={block}
        selected={p.selected}
        onSelect={p.onSelect}
        onCellText={p.onCellText}
        onAddRow={p.onAddRow}
        onRemoveRow={p.onRemoveRow}
        onSetLoopRow={p.onSetLoopRow}
        onCellFocus={p.onCellFocus}
        focusedCell={p.focusedCell}
      />
    );
  }

  // heading / paragraph：可编辑文本，按文档自带的段落/字符格式渲染
  const text = block.text;
  const marker = isMarker(text);
  const css = paraCss(block.fmt);
  const basePt = baseFontPt(block.runs);
  const hasRuns = !!(block.runs && block.runs.length > 0);
  // 没有 fmt 时也要给一个基线字号，否则会落到浏览器默认 16px，中文显得又大又散
  if (!block.fmt?.line_height && !block.fmt?.line_height_mm) css.lineHeight = 1.2;
  if (hasRuns || block.type === 'paragraph') {
    // 有 run 就用 run 的字号；纯文本段落也给 10.5pt 基线。
    // 标题在没有 run 时不设内联字号，交给 .df-block-heading-N 的兜底层级 ——
    // 内联值会盖掉那些类，结果标题比正文还小。
    css.fontSize = ptToPx(basePt);
  }
  if (block.type === 'heading' && !hasRuns) css.fontWeight = 700;

  return (
    <div
      className={`df-block ${block.type === 'heading' ? `df-block-heading-${block.level}` : ''} ${marker ? 'is-marker' : ''} ${p.selected ? 'selected' : ''}`}
      onClick={p.onSelect}
      style={css}
    >
      <EditableText
        text={text}
        runs={block.runs}
        selected={p.selected}
        onChange={p.onUpdateText}
      />
    </div>
  );
}

/**
 * 签章缩略图：把「这里将来会盖一枚多大的章」直接画出来。
 *
 * 为什么不只显示文字标签：用户抱怨的正是「看不出会不会撑坏版面」。
 * 给一个虚线框加名字，等于让他自己脑补尺寸；给出等比缩放后的真图，
 * 一眼就知道它和这一行的关系。
 * 加载失败（没权限 / 文件缺失）时退回文字标签，不能留一个破图。
 */
function SealThumb({ src, label, wPx, hPx }: { src: string; label: string; wPx: number; hPx: number }) {
  const [failed, setFailed] = useState(false);
  if (failed) {
    return (
      <span className="df-block-placeholder" style={{ width: wPx, height: hPx }}>
        {label}
      </span>
    );
  }
  return (
    <span className="df-ph-seal" style={{ width: wPx, height: hPx }} title={label}>
      <img src={src} alt={label} onError={() => setFailed(true)} />
    </span>
  );
}

function kindLabel(kind?: string): string {
  if (kind === 'image') return '图片';
  if (kind === 'qrcode') return '二维码';
  if (kind === 'rich') return '富文本';
  return '占位符';
}

// V3 签名/印章：复用 image 占位块，按绑定字段显示专属标签
function sealBadge(kind: string | undefined, isSig: boolean, isSeal: boolean): string {
  if (isSig) return '签名';
  if (isSeal) return '印章';
  return kindLabel(kind);
}
function sealLabel(block: { name: string; signature_id?: string; seal_id?: string }, isSig: boolean, isSeal: boolean): string {
  if (isSig) {
    const dn = lookupSealName('signature', block.signature_id!);
    return dn ? `签名：${dn}` : `签名占位 ${block.name}`;
  }
  if (isSeal) {
    const dn = lookupSealName('seal', block.seal_id!);
    return dn ? `印章：${dn}` : `印章占位 ${block.name}`;
  }
  return `{{${block.name}}}`;
}

// 可编辑文本：选中态用 textarea，否则按 run 渲染（字号/粗体/颜色都来自文档）
function EditableText({
  text,
  runs,
  selected,
  onChange,
}: {
  text: string;
  runs?: { text: string; size_pt?: number; bold?: boolean; italic?: boolean; underline?: boolean; color?: string; font?: string }[];
  selected: boolean;
  onChange: (t: string) => void;
}) {
  const ref = useRef<HTMLTextAreaElement>(null);
  const [draft, setDraft] = useState(text);
  useEffect(() => setDraft(text), [text]);
  useEffect(() => {
    if (ref.current) {
      ref.current.style.height = 'auto';
      ref.current.style.height = ref.current.scrollHeight + 'px';
    }
  }, [draft, selected]);

  if (selected) {
    return (
      <textarea
        ref={ref}
        rows={1}
        value={draft}
        autoFocus
        onChange={(e) => {
          setDraft(e.target.value);
          onChange(e.target.value);
        }}
        onClick={(e) => e.stopPropagation()}
      />
    );
  }
  if (runs && runs.length > 0) {
    return (
      <>
        {runs.map((r, i) => (
          <span key={i} style={runCss(r)}>
            <RunText text={r.text} />
          </span>
        ))}
      </>
    );
  }
  return (
    <>
      {segmentTokens(text, undefined).map((s, i) =>
        s.kind === 'plain' ? (
          <span key={i}>{s.text}</span>
        ) : (
          <span key={i} className={`tok-${s.kind}`}>{s.text}</span>
        ),
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// 表格：还原列宽与合并单元格
//
// 老实现是一个裸 <table>，没有 colgroup，浏览器就按内容自动分配列宽 ——
// 同一张表每一行列宽都不一样；合并单元格（gridSpan / vMerge）也没还原，
// 于是纵向合并的标签格被拆成几个空格子。这两条是「格式完全是乱的」里
// 最刺眼的部分。
// ---------------------------------------------------------------------------
function TableView({
  block,
  selected,
  onSelect,
  onCellText,
  onAddRow,
  onRemoveRow,
  onSetLoopRow,
  onCellFocus,
  focusedCell,
}: {
  block: Extract<EditBlock, { type: 'table' }>;
  selected: boolean;
  onSelect: () => void;
  onCellText: (t: string, r: number, c: number, text: string) => void;
  onAddRow: (t: string, r: number, w: 'above' | 'below') => void;
  onRemoveRow: (t: string, r: number) => void;
  onSetLoopRow: (t: string, r: number, v: boolean, loop?: { var: string; iterable: string }) => void;
  onCellFocus?: (t: string, r: number, c: number) => void;
  focusedCell?: { tableId: string; rowIdx: number; cellIdx: number } | null;
}) {
  const [activeCell, setActiveCell] = useState<{ r: number; c: number } | null>(null);
  const totalMm = tableWidthMm(block.col_width_mm, block.width_mm);
  const tableStyle: CSSProperties = {};
  if (totalMm) tableStyle.width = mmToPx(totalMm);
  if (block.col_width_mm && block.col_width_mm.length > 0) {
    // 有 tblGrid 就锁死布局，否则 colgroup 的宽度会被浏览器按内容二次分配
    tableStyle.tableLayout = 'fixed';
  }
  if (block.align === 'center') {
    tableStyle.marginLeft = 'auto';
    tableStyle.marginRight = 'auto';
  }
  if (block.align === 'right') {
    tableStyle.marginLeft = 'auto';
    tableStyle.marginRight = 0;
  }

  return (
    <div className={`df-block ${selected ? 'selected' : ''}`} onClick={onSelect}>
      <table className="df-table" style={tableStyle}>
        {block.col_width_mm && block.col_width_mm.length > 0 && (
          <colgroup>
            {block.col_width_mm.map((w, i) => (
              <col key={i} style={{ width: mmToPx(w) }} />
            ))}
          </colgroup>
        )}
        <tbody>
          {block.rows.map((row, ri) => (
            <RowView
              key={row.id}
              tableId={block.id}
              row={row}
              rowIdx={ri}
              onCellText={onCellText}
              onAddRow={onAddRow}
              onRemoveRow={onRemoveRow}
              onSetLoopRow={onSetLoopRow}
              activeCell={activeCell}
              setActiveCell={setActiveCell}
              onCellFocus={onCellFocus}
              focusedCell={focusedCell}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function RowView({
  tableId,
  row,
  rowIdx,
  onCellText,
  onAddRow,
  onRemoveRow,
  onSetLoopRow,
  activeCell,
  setActiveCell,
  onCellFocus,
  focusedCell,
}: {
  tableId: string;
  row: EditRow;
  rowIdx: number;
  onCellText: (t: string, r: number, c: number, text: string) => void;
  onAddRow: (t: string, r: number, w: 'above' | 'below') => void;
  onRemoveRow: (t: string, r: number) => void;
  onSetLoopRow: (t: string, r: number, v: boolean, loop?: { var: string; iterable: string }) => void;
  activeCell: { r: number; c: number } | null;
  setActiveCell: (v: { r: number; c: number } | null) => void;
  onCellFocus?: (t: string, r: number, c: number) => void;
  focusedCell?: { tableId: string; rowIdx: number; cellIdx: number } | null;
}) {
  return (
    <tr
      className={row.loop_row ? 'loop-row' : ''}
      style={{ position: 'relative' }}
      onMouseEnter={(e) => {
        const bar = e.currentTarget.querySelector('.df-row-actions') as HTMLElement | null;
        if (bar) bar.style.opacity = '1';
      }}
      onMouseLeave={(e) => {
        const bar = e.currentTarget.querySelector('.df-row-actions') as HTMLElement | null;
        if (bar) bar.style.opacity = '0';
      }}
    >
      {row.cells.map((cell, ci) => {
        // 被纵向合并吞掉的格不渲染，但它仍然留在 cells 数组里 ——
        // docapply 按「数组下标」定位 w:tc，删掉会让后面的格子整体错位。
        if (cell.hidden) return null;
        const isActive = activeCell && activeCell.r === rowIdx && activeCell.c === ci;
        const isFocused =
          focusedCell && focusedCell.tableId === tableId
          && focusedCell.rowIdx === rowIdx && focusedCell.cellIdx === ci;
        const tdStyle: CSSProperties = {};
        if (cell.shading) tdStyle.background = cell.shading;
        if (cell.valign) tdStyle.verticalAlign = cell.valign;
        return (
          <td
            key={ci}
            colSpan={cell.grid_span && cell.grid_span > 1 ? cell.grid_span : undefined}
            rowSpan={cell.row_span && cell.row_span > 1 ? cell.row_span : undefined}
            style={tdStyle}
            className={isFocused ? 'cell-focused' : ''}
          >
            {ci === 0 && (
              <div
                className="df-row-actions"
                style={{
                  position: 'absolute',
                  top: -24,
                  left: 0,
                  opacity: 0,
                  transition: 'opacity .12s',
                  background: '#fff',
                  border: '1px solid var(--app-border)',
                  borderRadius: 6,
                  boxShadow: 'var(--shadow-1)',
                  padding: '2px',
                  display: 'flex',
                  gap: 2,
                  zIndex: 15,
                }}
              >
                <button title="上方插入行" onClick={(e) => { e.stopPropagation(); onAddRow(tableId, rowIdx, 'above'); }}>+上</button>
                <button title="下方插入行" onClick={(e) => { e.stopPropagation(); onAddRow(tableId, rowIdx, 'below'); }}>+下</button>
                <button title="删除此行" onClick={(e) => { e.stopPropagation(); onRemoveRow(tableId, rowIdx); }}>删</button>
                <button
                  title="标记/取消循环行"
                  onClick={(e) => {
                    e.stopPropagation();
                    if (row.loop_row) {
                      onSetLoopRow(tableId, rowIdx, false);
                    } else {
                      const v = prompt('循环变量名（如 item）', 'item');
                      if (!v) return;
                      const it = prompt('可迭代对象名（如 items）', 'items');
                      if (!it) return;
                      onSetLoopRow(tableId, rowIdx, true, { var: v, iterable: it });
                    }
                  }}
                >
                  {row.loop_row ? '取消循环' : '循环行'}
                </button>
              </div>
            )}
            {ci === 0 && row.loop_row && (
              <span className="df-row-badge">循环行 {row.loop?.var || 'item'} in {row.loop?.iterable || 'items'}</span>
            )}
            {isActive ? (
              <CellEditor
                initial={cellText(cell)}
                onDone={(t) => {
                  onCellText(tableId, rowIdx, ci, t);
                  setActiveCell(null);
                }}
              />
            ) : (
              <div
                className="df-cell-body"
                onClick={(e) => {
                  e.stopPropagation();
                  setActiveCell({ r: rowIdx, c: ci });
                  onCellFocus?.(tableId, rowIdx, ci);
                }}
              >
                {cell.blocks.map((b, bi) => (
                  <CellBlock key={bi} block={b} />
                ))}
              </div>
            )}
          </td>
        );
      })}
    </tr>
  );
}

/** 单元格里第一个段落的文字（进入编辑时的初值）。 */
function cellText(cell: { blocks: EditCellBlock[] }): string {
  return cell.blocks[0]?.text ?? '';
}

/** 单元格内容渲染：按块真实的段落格式来，不再统一 13px。 */
function CellBlock({ block }: { block: EditCellBlock }) {
  if (block.type === 'placeholder') {
    const box = fitBox(block.width_mm, block.height_mm, 60, 40);
    const label = block.signature_id
      ? `签名：${lookupSealName('signature', block.signature_id) || block.name}`
      : block.seal_id
        ? `印章：${lookupSealName('seal', block.seal_id) || block.name}`
        : `{{${block.name}}}`;
    if (block.signature_id || block.seal_id) {
      return (
        <SealThumb
          src={`/api/v1/${block.signature_id ? 'signatures' : 'seals'}/${block.signature_id || block.seal_id}/image`}
          label={label}
          wPx={mmToPx(box.w)}
          hPx={mmToPx(box.h)}
        />
      );
    }
    return (
      <span className="df-block-placeholder" style={{ width: mmToPx(box.w), height: mmToPx(box.h) }}>
        {label}
      </span>
    );
  }
  const css = paraCss(block.fmt);
  if (!block.fmt?.line_height && !block.fmt?.line_height_mm) css.lineHeight = 1.2;
  css.fontSize = ptToPx(baseFontPt(block.runs));
  if (block.type === 'unsupported') {
    return <span className="df-cell-unsupported">{block.text || '（保留原样）'}</span>;
  }
  return (
    <div className="df-cell-para" style={css}>
      {block.runs && block.runs.length > 0 ? (
        block.runs.map((r, ri) => (
          <span key={ri} style={runCss(r)}>
            <RunText text={r.text} />
          </span>
        ))
      ) : (
        <RunText text={block.text || ''} />
      )}
    </div>
  );
}

function CellEditor({ initial, onDone }: { initial: string; onDone: (t: string) => void }) {
  const [v, setV] = useState(initial);
  return (
    <input
      autoFocus
      value={v}
      onChange={(e) => setV(e.target.value)}
      onBlur={() => onDone(v)}
      onKeyDown={(e) => {
        if (e.key === 'Enter') onDone(v);
        if (e.key === 'Escape') onDone(initial);
      }}
      style={{ width: '100%', border: '1px solid rgb(var(--primary-6))', borderRadius: 4, padding: 2, fontSize: 13 }}
    />
  );
}
