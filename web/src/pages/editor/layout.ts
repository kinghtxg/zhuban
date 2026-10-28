// 版式换算：OOXML 的毫米/磅 -> CSS 像素，以及块级样式合成。
//
// 为什么单独一个文件：这是「画布像不像 Word」的全部换算规则，散在
// BlockView 里会变成一堆魔法数字。集中在这里，出偏差时只改一处。
//
// 一个反直觉但必须交代的点：Word 的「1.0 倍行距」不等于 CSS 的 line-height:1。
// Word 的倍数行距乘以的是字体的行高（宋体约 1.2em），CSS 的 1 则是一整个 em。
// 直接照搬 1.0 会让中文行距明显偏紧、上下行贴在一起。所以倍数统一乘系数
// WORD_LINE_FACTOR 再交给 CSS。
import type { CSSProperties } from 'react';
import type { EditFmt, EditRun, PageGeometry } from '@/types/editor';

/** 96dpi 下 1mm 对应的像素数 */
export const MM_TO_PX = 96 / 25.4;
/** 1 磅 = 1/72 英寸 */
export const PT_TO_PX = 96 / 72;
/** Word 倍数行距 -> CSS line-height 的换算系数（见文件头说明） */
const WORD_LINE_FACTOR = 1.2;

export const mmToPx = (mm: number): number => Math.round(mm * MM_TO_PX * 100) / 100;
export const ptToPx = (pt: number): number => Math.round(pt * PT_TO_PX * 100) / 100;

export const A4_DEFAULT: Required<PageGeometry> = {
  width_mm: 210,
  height_mm: 297,
  margin_top_mm: 25.4,
  margin_right_mm: 31.8,
  margin_bottom_mm: 25.4,
  margin_left_mm: 31.8,
};

/** 补齐缺省字段，前端永远拿到完整的页面几何。 */
export function normalizePage(p?: PageGeometry): Required<PageGeometry> {
  return {
    width_mm: p?.width_mm ?? A4_DEFAULT.width_mm,
    height_mm: p?.height_mm ?? A4_DEFAULT.height_mm,
    margin_top_mm: p?.margin_top_mm ?? A4_DEFAULT.margin_top_mm,
    margin_right_mm: p?.margin_right_mm ?? A4_DEFAULT.margin_right_mm,
    margin_bottom_mm: p?.margin_bottom_mm ?? A4_DEFAULT.margin_bottom_mm,
    margin_left_mm: p?.margin_left_mm ?? A4_DEFAULT.margin_left_mm,
  };
}

/**
 * 段落级 CSS。
 *
 * 没有 fmt 的段落按 Word 的「单倍行距 + 零段距」处理：中文 Word 的 Normal
 * 样式就是这样，这也是老版本一律 line-height:1.7 看起来"松松垮垮"的原因。
 */
export function paraCss(fmt?: EditFmt): CSSProperties {
  const css: CSSProperties = {};
  if (!fmt) {
    css.lineHeight = 1;
    css.margin = 0;
    return css;
  }
  if (fmt.align) css.textAlign = fmt.align;
  if (fmt.indent_left_mm) css.paddingLeft = mmToPx(fmt.indent_left_mm);
  if (fmt.indent_right_mm) css.paddingRight = mmToPx(fmt.indent_right_mm);
  if (fmt.indent_first_mm) css.textIndent = mmToPx(fmt.indent_first_mm);
  if (fmt.space_before_mm) css.marginTop = mmToPx(fmt.space_before_mm);
  if (fmt.space_after_mm) css.marginBottom = mmToPx(fmt.space_after_mm);
  if (fmt.line_height_mm) {
    // 固定/最小值行距：Word 里是绝对值，直接用毫米换像素
    css.lineHeight = `${mmToPx(fmt.line_height_mm)}px`;
  } else if (fmt.line_height) {
    css.lineHeight = fmt.line_height * WORD_LINE_FACTOR;
  }
  return css;
}

/** 单个 run 的字符样式。 */
export function runCss(r: EditRun): CSSProperties {
  const css: CSSProperties = {};
  if (r.size_pt) css.fontSize = ptToPx(r.size_pt);
  if (r.bold) css.fontWeight = 700;
  if (r.italic) css.fontStyle = 'italic';
  if (r.underline) css.textDecoration = 'underline';
  if (r.color) css.color = r.color;
  if (r.font) css.fontFamily = `"${r.font}", var(--font-family-cn, inherit)`;
  return css;
}

/** 段落的基础字号：取第一个有字号的 run，没有就按 Word 默认 10.5pt（五号）。 */
export function baseFontPt(runs?: EditRun[]): number {
  const hit = runs?.find((r) => r.size_pt);
  return hit?.size_pt ?? 10.5;
}

/**
 * 把图片装进一个可用空间里，保持宽高比。
 *
 * 为什么必须保持比例：签名/印章的原始宽高比是「像不像」的前提。老实现把
 * 记录的 width_mm/height_mm 同时写进 wp:extent，比例不一致时图就被压扁了。
 *
 * @returns 适配后的 { w, h }（单位 mm）
 */
export function fitBox(
  wmm: number | undefined,
  hmm: number | undefined,
  maxW: number,
  maxH: number,
): { w: number; h: number } {
  // 缺一维就按正方形估：印章常见 1:1，签名的记录里通常两维都有
  let w = wmm && wmm > 0 ? wmm : (hmm && hmm > 0 ? hmm : maxH);
  let h = hmm && hmm > 0 ? hmm : w;
  const k = Math.min(1, maxW / w, maxH / h);
  w *= k;
  h *= k;
  return { w, h };
}

/** 一张表在页面上的总宽（mm）：优先用列宽之和，其次用表格自身宽度。 */
export function tableWidthMm(colWidths?: number[], widthMm?: number): number | undefined {
  if (colWidths && colWidths.length > 0) {
    return Math.round(colWidths.reduce((a, b) => a + b, 0) * 100) / 100;
  }
  return widthMm;
}
