import { NodeViewWrapper, type NodeViewProps } from '@tiptap/react';
import { Message } from '@arco-design/web-react';
import { IconDelete, IconImage, IconPlus } from '@arco-design/web-react/icon';

/**
 * 骨架标题的外观。
 *
 * 这里渲染的全是 `contentEditable={false}` 的静态内容——标题本身不可编辑，
 * 但右侧允许出现操作按钮（加图片、编辑图表数据、删掉这一条）。按钮必须拦住
 * 事件冒泡，否则点一下会先把光标送进编辑器、再触发按钮，选区就跳走了。
 */

/** 由编辑器注入到 storage，NodeView 用它回调外层做「选图」「开图表」这类动作。 */
export interface FillSkeletonStorage {
  onAddImage?: (path: string) => void;
  onOpenChart?: (path: string) => void;
  onRemoveItem?: (itemPath: string, index: number) => void;
  /** 当前是否有图片正在上传，用于禁按钮。 */
  busy?: boolean;
}

declare module '@tiptap/core' {
  interface Storage {
    fillSkeleton: FillSkeletonStorage;
  }
}

export function FillHeadingView({ node, editor }: NodeViewProps) {
  const a = node.attrs as Record<string, any>;
  const storage: FillSkeletonStorage = (editor.storage as any)?.fillSkeleton ?? {};
  const level = Number(a.level ?? 2);
  const slot = String(a.slot ?? 'scalar');
  const isItem = slot === 'item';
  const enumVals: string[] = Array.isArray(a.enum) ? a.enum : [];

  const stop = (e: React.MouseEvent) => {
    // 只拦冒泡，不 preventDefault：在 contentEditable=false 的节点上，
    // mousedown 的 preventDefault 会让浏览器认为这次按下「没有默认行为」，
    // 后续的 click 直接不派发——按钮看起来能点，但什么都不发生。
    e.stopPropagation();
  };

  /**
   * 点枚举值直接填进下面的正文。
   *
   * 原来只是一个灰色提示（「可选：高 / 中 / 低」），用户照着敲——敲成「高风险」
   * 就完了：模板里的 `{% if 总体危险程度 == "高" %}` 不成立，那一段内容整块
   * 消失，而界面上没有任何提示。这类错误在审阅时极难发现，因为它不报错。
   *
   * 所以改成点选：值只有一种写法，写不错。
   */
  const applyEnum = (val: string) => {
    // 按 path 找位置，不用 getPos()。
    //
    // ReactNodeViewRenderer 给过来的 getPos 在这个场景里拿不到可靠位置（点按钮时
    // 选区并不在这个节点上），而 path 在全文档里唯一——遍历一遍更慢但不会错。
    // 50 多个节点遍历一次是微秒级，用户点的是一次按钮，不是每秒一次。
    let pos: number | null = null;
    editor.state.doc.forEach((n, offset) => {
      if (pos == null && n.type.name === 'fillHeading' && n.attrs.path === String(a.path)) {
        pos = offset;
      }
    });
    if (pos == null) return;
    const { state } = editor;
    const after = pos + node.nodeSize;
    const next = state.doc.nodeAt(after);
    if (!next || next.type.name !== 'paragraph') return;
    const from = after + 1;
    const to = after + next.nodeSize - 1;
    const tr = state.tr.replaceWith(from, to, val ? state.schema.text(val) : []);
    editor.view.dispatch(tr.scrollIntoView());
    Message.success(`已填入「${val}」`);
  };

  return (
    <NodeViewWrapper
      className={`df-mf-h df-mf-h--l${level}${isItem ? ' is-item' : ''}`}
      contentEditable={false}
      data-slot={slot}
      data-path={a.path}
    >
      <div className="df-mf-h__row">
        <span className="df-mf-h__label">
          {a.label}
          {a.required && !isItem ? <i className="df-mf-h__req" title="必填" /> : null}
        </span>

        {a.hint ? <span className="df-mf-h__hint">{a.hint}</span> : null}

        <span className="df-mf-h__acts">
          {(slot === 'image' || slot === 'images') && storage.onAddImage ? (
            <button
              type="button"
              className="df-mf-h__btn"
              disabled={!!storage.busy}
              onMouseDown={stop}
              onClick={(e) => {
                stop(e);
                storage.onAddImage?.(String(a.path));
              }}
            >
              <IconImage /> 添加图片
            </button>
          ) : null}

          {slot === 'chart' && storage.onOpenChart ? (
            <button
              type="button"
              className="df-mf-h__btn"
              onMouseDown={stop}
              onClick={(e) => {
                stop(e);
                storage.onOpenChart?.(String(a.path));
              }}
            >
              <IconPlus /> 表格填数据
            </button>
          ) : null}

          {isItem && storage.onRemoveItem ? (
            <button
              type="button"
              className="df-mf-h__btn df-mf-h__btn--danger"
              title="删掉这一条，连同它下面填的内容"
              onMouseDown={stop}
              onClick={(e) => {
                stop(e);
                storage.onRemoveItem?.(String(a.itemPath), Number(a.itemIndex));
              }}
            >
              <IconDelete /> 删这一条
            </button>
          ) : null}
        </span>
      </div>

      {enumVals.length > 0 ? (
        <div className="df-mf-h__enum">
          {enumVals.map((v) => (
            <button
              key={v}
              type="button"
              className="df-mf-h__chip"
              title={`点一下把「${v}」填到下面`}
              // 用 mousedown 而不是 click：ProseMirror 在 atom 节点上会先处理
              // 鼠标按下，click 走不到 React。mousedown 是能收到的最早时机。
              onMouseDown={(e) => {
                e.stopPropagation();
                applyEnum(v);
              }}
            >
              {v}
            </button>
          ))}
        </div>
      ) : null}
    </NodeViewWrapper>
  );
}
