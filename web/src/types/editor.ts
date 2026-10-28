// 在线编辑器块模型 —— 严格对齐 render-service/app/docmodel.py（parse 输出）与 docapply.py（apply 契约）。
// 禁止凭 PRD 臆造字段；此处字段与 render-service 真实 JSON 一一对应。

export type EditTokenKind = 'var' | 'block' | 'comment';
export interface EditToken {
  start: number;
  end: number;
  kind: EditTokenKind;
  text: string;
}

export type PlaceholderKind = 'scalar' | 'image' | 'qrcode' | 'rich';

// ---------------------------------------------------------------------------
// 版式（V4，docmodel._para_format / _paragraph_runs 输出）
//
// 老模型只有 text，画布上所有段落都是「左对齐 14px 无缩进」，用户反馈
// 「格式完全是乱的」。这几个字段就是把 Word 的排版信息带过来。
// 单位统一用毫米（mm），字号用磅（pt），与 OOXML 原值换算在服务端完成。
// ---------------------------------------------------------------------------
export interface EditFmt {
  align?: 'left' | 'right' | 'center' | 'justify';
  indent_left_mm?: number;
  indent_right_mm?: number;
  /** 首行缩进；负值表示悬挂缩进 */
  indent_first_mm?: number;
  space_before_mm?: number;
  space_after_mm?: number;
  /** 倍数行距（w:lineRule=auto，360 = 1.5 倍） */
  line_height?: number;
  /** 固定/最小值行距（mm） */
  line_height_mm?: number;
}

/** 一个 run 的有效字符格式。缺省字段表示继承段落样式。 */
export interface EditRun {
  text: string;
  size_pt?: number;
  bold?: boolean;
  italic?: boolean;
  underline?: boolean;
  color?: string;
  font?: string;
}

export interface PageGeometry {
  width_mm?: number;
  height_mm?: number;
  margin_top_mm?: number;
  margin_right_mm?: number;
  margin_bottom_mm?: number;
  margin_left_mm?: number;
}

// 单元格内段落块（与 docmodel._parse_cell 输出一致）
export interface EditCellBlock {
  id: string;
  type: 'paragraph' | 'placeholder' | 'unsupported';
  text?: string;
  tokens?: EditToken[];
  fmt?: EditFmt;
  runs?: EditRun[];
  note?: string;
  kind?: PlaceholderKind;
  name?: string;
  width_mm?: number;
  height_mm?: number;
  asset_id?: string;
  // V3 签名/印章：复用 image 占位块，按 id 前缀 + 绑定字段由后端解析注入
  signature_id?: string;
  seal_id?: string;
}

export interface EditCell {
  blocks: EditCellBlock[];
  /** 跨列数（w:gridSpan），缺省 1 */
  grid_span?: number;
  /** 纵向合并：restart 是合并块的起点，continue 是被吞掉的那几格 */
  v_merge?: 'restart' | 'continue';
  /** 起点格跨越的行数（服务端算好） */
  row_span?: number;
  /** continue 格：渲染时跳过，但**必须留在 cells 数组里**，docapply 按索引写格 */
  hidden?: boolean;
  shading?: string;
  valign?: 'top' | 'center' | 'bottom';
}

export interface EditLoop {
  var: string;
  iterable: string;
}

export interface EditRow {
  id: string;
  cells: EditCell[];
  loop_row?: boolean;
  loop?: EditLoop;
}

// 顶层块（docmodel.parse_docx 的 blocks 元素）
export type EditBlock =
  | { id: string; type: 'heading'; level: number; text: string; tokens?: EditToken[]; fmt?: EditFmt; runs?: EditRun[] }
  | { id: string; type: 'paragraph'; text: string; tokens?: EditToken[]; fmt?: EditFmt; runs?: EditRun[] }
  | {
      id: string;
      type: 'list';
      ordered: boolean;
      items: { text: string; tokens?: EditToken[]; fmt?: EditFmt; runs?: EditRun[] }[];
    }
  | {
      id: string;
      type: 'table';
      rows: EditRow[];
      /** 各逻辑列宽度（mm），来自 w:tblGrid */
      col_width_mm?: number[];
      /** 表格是否固定布局（w:tblLayout=fixed） */
      fixed_layout?: boolean;
      width_mm?: number;
      align?: 'left' | 'center' | 'right';
    }
  | {
      id: string;
      type: 'placeholder';
      kind: PlaceholderKind;
      name: string;
      width_mm?: number;
      height_mm?: number;
      asset_id?: string;
      signature_id?: string;
      seal_id?: string;
      fmt?: EditFmt;
    }
  | { id: string; type: 'unsupported'; note: string; text?: string }
  // 内联图表：不可在线改样式，但数据可通过变量传参替换（见 chartdata.py）
  | {
      id: string;
      type: 'chart';
      chart_part: string;
      chart_kind: string;
      kind_name: string;
      title: string;
      variable: string;
      series_count: number;
      point_count: number;
      categories: string[];
      series: { name: string; values: string[] }[];
      note?: string;
    };

export interface EditorModelResp {
  blocks: EditBlock[];
  warnings: string[];
  /** 纸张尺寸与页边距（mm）。缺失时前端按 A4 默认值兜底。 */
  page?: PageGeometry;
  version_id: string;
  version_no: string;
}

export interface EditorApplyResp {
  preview_id: string;
  pages: { page: number; url: string; width?: number; height?: number }[];
  expires_at?: string;
}

// 版本时间线条目（versionMap 输出）
export interface EditorVersion {
  id: string;
  version: string;
  is_current: boolean;
  source: 'upload' | 'online_edit' | 'rollback';
  change_summary?: string;
  created_by?: string;
  created_at?: string;
  file_size?: number;
}
export interface EditorVersionListResp {
  items: EditorVersion[];
}
