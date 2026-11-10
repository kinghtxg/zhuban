import { useEffect, useRef, useState } from 'react';
import { Button, Message, Tooltip } from '@arco-design/web-react';
import { IconImage, IconRedo, IconUndo } from '@arco-design/web-react/icon';
import { fileToDataUrl } from '@/utils/imagefile';
import { mdToHtmlLite, sanitizeRichHtml } from '@/utils/richtext';
import './richfield.css';

/**
 * 富文本字段编辑器（contenteditable）。
 *
 * 为什么不能再是一个 TextArea：
 * 安全报告的主体就是截图。只要不能贴图，用户就得「先导出再去 Word 里插图」，
 * 那一步一插，版面就再也不是模板的了——他会调字号、调间距、拖图片大小，
 * 最后拿到的文档跟模板毫无关系。所以"能在里面贴图"不是锦上添花，
 * 而是**保住模板格式的前提**。
 *
 * 三条硬约束，都在这里兜住：
 *
 * 1. **只放行语义格式**。加粗/斜体/下划线/删除线/颜色/图片。字体、字号、
 *    行距、对齐、缩进一概不采集——那是模板的段落属性说了算。见 utils/richtext
 *    的白名单，粘贴 Word 内容时那一堆 `font-family:宋体` 会被剥掉。
 * 2. **粘贴默认走纯文本**。从 Word/网页粘进来的是整段带样式的 HTML，
 *    字面看着一样，实际会把模板字体顶掉。文字转纯文本、图片单独取出来，
 *    用户想加粗自己按按钮。
 * 3. **值永远是 `{type:'html', content}`**。渲染服务认这个形状，走
 *    richdata.html_to_richtext，注入时只改被覆盖 run 的 `<w:t>`，
 *    段落的 `w:pPr`（居中/缩进/行距）一个字节都不动。
 */

export interface RichTextFieldProps {
  /** 当前值：`{type:'html'|'markdown', content}` 或历史遗留的裸字符串 */
  value: unknown;
  onChange: (next: { type: 'html'; content: string }) => void;
  placeholder?: string;
  /** 校验没过时描红边 */
  error?: boolean;
  minRows?: number;
}

function asObject(v: unknown): Record<string, unknown> {
  return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {};
}

/**
 * 把存着的值渲染成编辑器里的 html。
 *
 * markdown 要经过 mdToHtmlLite 升一级，理由见那个函数的注释：不升，
 * 用户当初写的 `**重点**` 会带着星号出现在正式报告里。
 */
function toEditorHtml(value: unknown): string {
  if (typeof value === 'string') return mdToHtmlLite(value);
  const obj = asObject(value);
  const content = typeof obj.content === 'string' ? obj.content : '';
  if (!content) return '';
  if (obj.type === 'markdown') return mdToHtmlLite(content);
  return sanitizeRichHtml(content);
}

export default function RichTextField({
  value,
  onChange,
  placeholder,
  error,
  minRows = 4,
}: RichTextFieldProps) {
  const boxRef = useRef<HTMLDivElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  // 记住"上次我们写进 DOM 的 html"。外部值没有变化时不要回写 innerHTML，
  // 否则用户每敲一个字光标都会跳到段首（contenteditable 被 React 重渲染的经典坑）。
  const lastHtml = useRef<string>('');
  const [empty, setEmpty] = useState(!toEditorHtml(value));
  const [focused, setFocused] = useState(false);

  const html = toEditorHtml(value);

  useEffect(() => {
    const box = boxRef.current;
    if (!box) return;
    if (html === lastHtml.current) return;
    // 只有在差异确实来自"外部"（填示例数据、自由写作应用建议、清空）时才回写
    if (sanitizeRichHtml(box.innerHTML) === html) {
      lastHtml.current = html;
      return;
    }
    box.innerHTML = html;
    lastHtml.current = html;
    setEmpty(!html);
  }, [html]);

  const emit = () => {
    const box = boxRef.current;
    if (!box) return;
    const clean = sanitizeRichHtml(box.innerHTML);
    lastHtml.current = clean;
    setEmpty(!clean);
    onChange({ type: 'html', content: clean });
  };

  /** 在光标处插入 html 片段，并触发一次回写。 */
  const insertHtml = (fragment: string) => {
    const box = boxRef.current;
    if (!box) return;
    box.focus();
    const sel = window.getSelection();
    const inside = sel && sel.rangeCount > 0 && box.contains(sel.getRangeAt(0).commonAncestorContainer);
    if (!inside) {
      // 没焦点/光标不在框里：追加到末尾。不用 innerHTML += 是为了不重建已有节点
      box.insertAdjacentHTML('beforeend', fragment);
    } else {
      const range = sel!.getRangeAt(0);
      range.deleteContents();
      const frag = range.createContextualFragment(fragment);
      const last = frag.lastChild;
      range.insertNode(frag);
      if (last) {
        const after = document.createRange();
        after.setStartAfter(last);
        after.collapse(true);
        sel!.removeAllRanges();
        sel!.addRange(after);
      }
    }
    emit();
  };

  const cmd = (name: string) => {
    boxRef.current?.focus();
    // execCommand 已标记废弃，但它是唯一零依赖、且在各浏览器里对
    // contenteditable 行为一致的方案。引入一整套编辑器框架只为了三个按钮
    // 不划算：那会带来它自己的段落模型，反而更容易把模板版面搅乱。
    document.execCommand(name, false);
    emit();
  };

  const pickImages = async (files: FileList | File[] | null) => {
    if (!files || !files.length) return;
    for (const f of Array.from(files)) {
      try {
        const src = await fileToDataUrl(f);
        insertHtml(`<img src="${src}"/>`);
      } catch (err) {
        Message.error(err instanceof Error ? err.message : '图片读取失败');
      }
    }
  };

  const onPaste = (e: React.ClipboardEvent<HTMLDivElement>) => {
    const items = e.clipboardData?.items;
    const imgs: File[] = [];
    if (items) {
      for (const it of Array.from(items)) {
        if (it.kind === 'file' && it.type.startsWith('image/')) {
          const f = it.getAsFile();
          if (f) imgs.push(f);
        }
      }
    }
    if (imgs.length) {
      e.preventDefault();
      void pickImages(imgs);
      return;
    }
    const text = e.clipboardData?.getData('text/plain');
    if (text) {
      // 纯文本粘贴：换行转 <br/>。绝不能让它把 Word 的 HTML 直接灌进来
      e.preventDefault();
      insertHtml(mdToHtmlLite(text));
    }
  };

  return (
    <div className={error ? 'df-rich is-error' : 'df-rich'}>
      <div className={focused ? 'df-rich-bar is-focus' : 'df-rich-bar'}>
        {/* onMouseDown 阻止默认：点按钮时不能把编辑器里的选区弄丢，
            否则 execCommand 作用不到任何地方 */}
        <Tooltip content="加粗">
          <button type="button" className="df-rich-btn" onMouseDown={(e) => e.preventDefault()} onClick={() => cmd('bold')}>
            <b>B</b>
          </button>
        </Tooltip>
        <Tooltip content="斜体">
          <button type="button" className="df-rich-btn" onMouseDown={(e) => e.preventDefault()} onClick={() => cmd('italic')}>
            <i>I</i>
          </button>
        </Tooltip>
        <Tooltip content="下划线">
          <button type="button" className="df-rich-btn" onMouseDown={(e) => e.preventDefault()} onClick={() => cmd('underline')}>
            <u>U</u>
          </button>
        </Tooltip>
        <Tooltip content="删除线">
          <button type="button" className="df-rich-btn" onMouseDown={(e) => e.preventDefault()} onClick={() => cmd('strikeThrough')}>
            <s>S</s>
          </button>
        </Tooltip>
        <span className="df-rich-sep" />
        <Tooltip content="插入图片">
          <button
            type="button"
            className="df-rich-btn"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => fileRef.current?.click()}
          >
            <IconImage />
          </button>
        </Tooltip>
        <Tooltip content="撤销">
          <button type="button" className="df-rich-btn" onMouseDown={(e) => e.preventDefault()} onClick={() => cmd('undo')}>
            <IconUndo />
          </button>
        </Tooltip>
        <Tooltip content="重做">
          <button type="button" className="df-rich-btn" onMouseDown={(e) => e.preventDefault()} onClick={() => cmd('redo')}>
            <IconRedo />
          </button>
        </Tooltip>
        <span className="df-rich-hint">粘贴文字会去掉原来的字体字号，格式由模板决定</span>
      </div>

      <div className="df-rich-wrap">
        {empty && !focused && placeholder ? (
          <div className="df-rich-ph">{placeholder}</div>
        ) : null}
        <div
          ref={boxRef}
          className="df-rich-box"
          style={{ minHeight: minRows * 24 }}
          contentEditable
          suppressContentEditableWarning
          spellCheck={false}
          onInput={emit}
          onPaste={onPaste}
          onFocus={() => setFocused(true)}
          onBlur={() => {
            setFocused(false);
            emit();
          }}
          onDrop={(e) => {
            const files = Array.from(e.dataTransfer.files || []).filter((f) => f.type.startsWith('image/'));
            if (files.length) {
              e.preventDefault();
              void pickImages(files);
            }
          }}
        />
      </div>

      <input
        ref={fileRef}
        type="file"
        accept="image/*"
        multiple
        style={{ display: 'none' }}
        onChange={(e) => {
          const files = e.target.files;
          e.target.value = ''; // 允许连选同一张
          void pickImages(files);
        }}
      />
      {empty ? null : (
        <div className="df-rich-foot">
          <Button
            size="mini"
            type="text"
            status="danger"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => {
              if (boxRef.current) boxRef.current.innerHTML = '';
              emit();
            }}
          >
            清空这一段
          </Button>
        </div>
      )}
    </div>
  );
}
