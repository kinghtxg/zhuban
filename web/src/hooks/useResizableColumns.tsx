/**
 * useResizableColumns —— 表格列宽拖动 + 持久化
 *
 * 为什么自己做：
 *   Arco Table 没有列宽拖动（`grep -r resizable es/Table/` 无结果）。
 *   但它留了两个口子可以拼出来：
 *     1. `column.onHeaderCell(column, index)` 的返回值会被 spread 到 `<th>` 上
 *        （`es/Table/thead/column.js` → `__assign({...}, thProps, headerCellProps)`）；
 *     2. `components={{ header: { th } }}` 可替换 `<th>` 本身
 *        （`es/Table/hooks/useComponent.js` → `ComponentTh`）。
 *   于是把「这列叫什么 + 拖它的时候干什么」塞进 headerCellProps，再由自定义
 *   `<th>` 在右侧渲染一根可拖的竖线，就得到了原生观感的列宽拖动。
 *
 * 为什么拖完要落 localStorage：
 *   列宽是用户对「我关心哪几列」的一次性取舍。刷新一次就丢，等于每进一次页面
 *   都要重调一遍，比不给拖更烦。
 *
 * ⚠️ 自定义 <th> 必须是**模块级**常量：
 *   写成组件内的内联函数，每次 render 都是新组件类型，React 会把整个表头
 *   卸载重建 —— 拖动时每移动一像素重建一次，肉眼可见地卡。
 */

import {
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
} from 'react';

/** 列定义只要求这几个字段，其余原样透传给 Arco */
export interface ResizableColumn {
  key?: string | number;
  dataIndex?: string | number;
  width?: number;
  /** 拖动下限，默认 64 —— 再窄表头文字就没法看了 */
  minWidth?: number;
  /** 拖动上限，默认 800 */
  maxWidth?: number;
  onHeaderCell?: (column: unknown, index: number) => Record<string, unknown>;
  [key: string]: unknown;
}

const DEFAULT_MIN_WIDTH = 64;
const DEFAULT_MAX_WIDTH = 800;
/** 键盘微调步长（按住 Shift 走大步） */
const KEY_STEP = 16;
const KEY_STEP_LARGE = 64;

function clampWidth(v: number, min: number, max: number) {
  return Math.min(max, Math.max(min, Math.round(v)));
}

/** 读不出来就当没存过：隐私模式下 localStorage 会直接抛，别让它把页面带崩 */
function loadWidths(storageKey: string): Record<string, number> {
  try {
    const raw = localStorage.getItem(storageKey);
    if (!raw) return {};
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
    const out: Record<string, number> = {};
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
      if (typeof v === 'number' && Number.isFinite(v)) out[k] = v;
    }
    return out;
  } catch {
    return {};
  }
}

function saveWidths(storageKey: string, widths: Record<string, number>) {
  try {
    localStorage.setItem(storageKey, JSON.stringify(widths));
  } catch {
    // 存不了就不存：功能退化，但不该报错给用户
  }
}

interface ResizableThProps {
  className?: string;
  style?: CSSProperties;
  children?: ReactNode;
  /** 有值才渲染拖拽手柄；分组表头的 th 没有这个字段 */
  'data-col-key'?: string;
  'data-col-width'?: number;
  onResizeStart?: (e: ReactMouseEvent<HTMLElement>, key: string) => void;
  onResizeStep?: (e: ReactKeyboardEvent<HTMLElement>, key: string, dir: -1 | 1) => void;
  onResizeReset?: (key: string) => void;
  // 用 any 而不是 unknown：这个对象要整个 spread 到 <th> 上，
  // unknown 值会让 TS 拒绝 spread 到 JSX 内在元素
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  [key: string]: any;
}

/**
 * 自定义表头单元格。
 * ⚠️ 必须把 onResizeStart / onResizeStep / onResizeReset / data-col-* 从 rest 里摘掉，
 *    否则会被当成未知 DOM 属性写到 <th> 上，React 会刷一屏警告。
 */
function ResizableTh({
  className,
  style,
  children,
  'data-col-key': colKey,
  'data-col-width': colWidth,
  onResizeStart,
  onResizeStep,
  onResizeReset,
  ...rest
}: ResizableThProps) {
  return (
    <th className={className ? `df-th ${className}` : 'df-th'} style={style} {...rest}>
      {children}
      {colKey ? (
        <span
          className="df-col-resizer"
          role="separator"
          aria-orientation="vertical"
          aria-label="拖动调整列宽，双击恢复默认"
          aria-valuenow={typeof colWidth === 'number' ? colWidth : undefined}
          tabIndex={0}
          title="拖动调整列宽，双击恢复默认"
          onMouseDown={(e) => onResizeStart?.(e, colKey)}
          onDoubleClick={(e) => {
            e.stopPropagation();
            onResizeReset?.(colKey);
          }}
          onKeyDown={(e) => {
            if (e.key === 'ArrowLeft') {
              e.preventDefault();
              onResizeStep?.(e, colKey, -1);
            } else if (e.key === 'ArrowRight') {
              e.preventDefault();
              onResizeStep?.(e, colKey, 1);
            }
          }}
        />
      ) : null}
    </th>
  );
}

export interface UseResizableColumnsResult<T> {
  /** 已完成宽度回填的列定义，直接交给 <Table columns={...} /> */
  columns: T[];
  /** 直接交给 <Table components={...} /> */
  components: { header: { th: typeof ResizableTh } };
  /** 所有列宽之和，作为 scroll.x 用 —— 不传的话列宽会被 table-layout 忽略 */
  totalWidth: number;
  /** 一键恢复默认列宽（页面可以挂一个「重置列宽」按钮） */
  resetAll: () => void;
}

/**
 * @param storageKey localStorage 键，按表格区分，例如 `df.cols.admin-users`
 * @param columns    原始列定义（可以每次 render 重新构造）
 */
export function useResizableColumns<T extends ResizableColumn>(
  storageKey: string,
  columns: T[],
): UseResizableColumnsResult<T> {
  const [widths, setWidths] = useState<Record<string, number>>(() => loadWidths(storageKey));

  // 拖动过程中 state 是异步的，但落库和夹取都要「最新值」，
  // 所以用 ref 做唯一事实来源，render 前先更新 ref。
  const widthsRef = useRef(widths);
  widthsRef.current = widths;

  /** 列 → 稳定标识。没有 key 时退回 dataIndex，再退回下标 */
  const keys = useMemo(
    () =>
      columns.map((c, i) =>
        String(c.key ?? c.dataIndex ?? `col-${i}`),
      ),
    // 列的数量与标识变了才重算；列对象每次 render 都是新的，不能进依赖
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [columns.map((c, i) => String(c.key ?? c.dataIndex ?? `col-${i}`)).join('|')],
  );

  const limitsRef = useRef(
    columns.map((c) => [
      typeof c.minWidth === 'number' ? c.minWidth : DEFAULT_MIN_WIDTH,
      typeof c.maxWidth === 'number' ? c.maxWidth : DEFAULT_MAX_WIDTH,
      typeof c.width === 'number' ? c.width : 0,
    ]),
  );
  limitsRef.current = columns.map((c) => [
    typeof c.minWidth === 'number' ? c.minWidth : DEFAULT_MIN_WIDTH,
    typeof c.maxWidth === 'number' ? c.maxWidth : DEFAULT_MAX_WIDTH,
    typeof c.width === 'number' ? c.width : 0,
  ]);

  const keyIndex = useMemo(() => {
    const m = new Map<string, number>();
    keys.forEach((k, i) => m.set(k, i));
    return m;
  }, [keys]);

  const commit = (key: string, next: number | null) => {
    const nextWidths = { ...widthsRef.current };
    if (next === null) delete nextWidths[key];
    else nextWidths[key] = next;
    widthsRef.current = nextWidths;
    setWidths(nextWidths);
    saveWidths(storageKey, nextWidths);
  };

  /** ref 里永远是「拖完那一版」，mousemove 只刷 state，松手再落库 */
  const handleResizeStart = (e: ReactMouseEvent<HTMLElement>, key: string) => {
    e.preventDefault();
    e.stopPropagation();
    const th = (e.currentTarget as HTMLElement).closest('th');
    const idx = keyIndex.get(key) ?? -1;
    const [min, max] = idx >= 0 ? limitsRef.current[idx].slice(0, 2) : [DEFAULT_MIN_WIDTH, DEFAULT_MAX_WIDTH];
    // 起点用「当前渲染宽度」而不是声明宽度：table-layout 会把声明宽度按比例
    // 拉伸，两者不等，用声明宽度会出现「按下手柄列宽先跳一下」
    const startWidth = th ? th.getBoundingClientRect().width : widthsRef.current[key] ?? 120;
    const startX = e.clientX;
    const body = document.body;
    const prevCursor = body.style.cursor;
    const prevSelect = body.style.userSelect;
    body.style.cursor = 'col-resize';
    body.style.userSelect = 'none';

    const onMove = (ev: MouseEvent) => {
      const w = clampWidth(startWidth + ev.clientX - startX, min, max);
      if (widthsRef.current[key] === w) return;
      const nextWidths = { ...widthsRef.current, [key]: w };
      widthsRef.current = nextWidths;
      setWidths(nextWidths);
    };
    const onUp = () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
      body.style.cursor = prevCursor;
      body.style.userSelect = prevSelect;
      saveWidths(storageKey, widthsRef.current);
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
  };

  const handleResizeStep = (e: ReactKeyboardEvent<HTMLElement>, key: string, dir: -1 | 1) => {
    const idx = keyIndex.get(key) ?? -1;
    const [min, max, fallback] = idx >= 0 ? limitsRef.current[idx] : [DEFAULT_MIN_WIDTH, DEFAULT_MAX_WIDTH, 120];
    const current = widthsRef.current[key] ?? fallback;
    const step = e.shiftKey ? KEY_STEP_LARGE : KEY_STEP;
    commit(key, clampWidth(current + dir * step, min, max));
  };

  const handleResizeReset = (key: string) => {
    commit(key, null);
  };

  const resetAll = () => {
    widthsRef.current = {};
    setWidths({});
    saveWidths(storageKey, {});
  };

  const merged = useMemo(
    () =>
      columns.map((col, i) => {
        const key = keys[i];
        const width = widths[key] ?? (typeof col.width === 'number' ? col.width : undefined);
        return {
          ...col,
          ...(width ? { width } : {}),
          onHeaderCell: () => ({
            'data-col-key': key,
            'data-col-width': width,
            onResizeStart: handleResizeStart,
            onResizeStep: handleResizeStep,
            onResizeReset: handleResizeReset,
          }),
        } as unknown as T;
      }),
    // widths/keyIndex 变了才重算；handleResize* 是每次 render 新建的闭包，
    // 但它们只依赖 ref，不会读到过期值
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [columns, widths, keys],
  );

  const totalWidth = useMemo(
    () =>
      merged.reduce((sum, c) => sum + (typeof c.width === 'number' ? c.width : 0), 0),
    [merged],
  );

  const components = useMemo(() => ({ header: { th: ResizableTh } }), []);

  return { columns: merged, components, totalWidth, resetAll };
}
