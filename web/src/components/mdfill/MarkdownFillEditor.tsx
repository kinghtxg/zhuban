import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useEditor, EditorContent } from '@tiptap/react';
import StarterKit from '@tiptap/starter-kit';
import Image from '@tiptap/extension-image';
import { Fragment } from '@tiptap/pm/model';
import { Button, Message, Select } from '@arco-design/web-react';
import { IconPlus } from '@arco-design/web-react/icon';

import type { TemplateVariable } from '@/types/api';
import { buildSkeleton, loopPaths, nodesForNewItem } from './skeleton';
import type { SkeletonHeading, SkeletonNode } from './skeleton';
import { extractData, numericPaths } from './extract';
import type { ExtractIssue } from './extract';
import { toDocJSON } from './toDoc';
import { fileToDataUrl } from '@/utils/imagefile';
import { FillHeading, SKELETON_EDIT_META } from './FillHeading';
import type { FillSkeletonStorage } from './FillHeadingView';
import './markdown-fill.css';

/**
 * 「像写文档一样填写」的编辑器。
 *
 * 和刻本 BlockEditor 的关系：交互手感（块操作、斜杠命令、查找、拖拽手柄）
 * 是从那边移植过来的，但领域功能（双链、目录、白板、AI 续写、离线草稿）
 * 全部去掉——铸版这里不是写文章，是填模板。见 README-编辑器来源.md。
 *
 * 对外只有三样东西：初始数据、变更回调、几个受控操作（加图片 / 加条目 /
 * 删条目）。**不暴露 Markdown 字符串**：编辑器的内部表示就是结构化文档，
 * 中间再走一道 Markdown 序列化，每条规则都是一个可能丢内容的地方
 * （空行、硬折行、`&` 转义），而这些恰恰是「格式变了」的常见来源。
 */

export interface MarkdownFillEditorHandle {
  /** 用一批新数据重建整份文档。会丢弃当前未保存的编辑。 */
  reset: (data?: Record<string, unknown>) => void;
  /** 在当前字段末尾插入一张图片。 */
  insertImage: (path: string, src: string) => void;
}

interface Props {
  variables: TemplateVariable[];
  /** 初始数据。只在挂载与 reset 时读，之后的编辑以文档为准。 */
  initialData?: Record<string, unknown>;
  onChange: (data: Record<string, unknown>, issues: ExtractIssue[]) => void;
  /** 点「添加图片」时回调外层（外层负责弹文件选择、压缩、转 dataURL）。 */
  onPickImage?: (path: string, multiple: boolean) => void;
  /** 点「表格填数据」时回调外层打开图表抽屉。 */
  onOpenChart?: (path: string, payload: unknown) => void;
  /** 编辑器就绪后把操作句柄交出去。 */
  onReady?: (handle: MarkdownFillEditorHandle) => void;
}

const EMPTY: Record<string, unknown> = {};

export default function MarkdownFillEditor({
  variables,
  initialData,
  onChange,
  onPickImage,
  onOpenChart,
  onReady,
}: Props) {
  const [charCount, setCharCount] = useState(0);
  const [busy] = useState(false);
  /**
   * 大纲的版本键。
   *
   * 一份渗透报告展开后是 50 多个骨架标题，滚到一半就不知道自己在哪个字段了，
   * 所以工具栏放了一个「跳到字段」。它必须跟着文档变（用户增删漏洞条目后
   * 列表要跟着变），但每次按键都重新遍历文档太浪费——用一个「标题数 + 末尾
   * 标题名」的键来判定结构是否真的变了。
   */
  const [outlineKey, setOutlineKey] = useState('0');

  // 初始数据只认第一次：之后 value 变化（比如父组件的 state 回流）不该重建
  // 文档——重建会把用户刚敲的字冲掉，而且他把光标停在某个字段上时突然
  // 整篇重排，看起来像编辑器自己抽风。
  const seedRef = useRef(initialData ?? EMPTY);
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;

  const skeleton = useMemo(() => buildSkeleton(variables, seedRef.current), [variables]);

  const emitRef = useRef<number | null>(null);
  const emit = useCallback((doc: any) => {
    if (emitRef.current) window.clearTimeout(emitRef.current);
    emitRef.current = window.setTimeout(() => {
      const { data, issues } = extractData(doc, numericPaths(seedRef.current), seedRef.current);
      onChangeRef.current(data, issues);
    }, 260);
  }, []);

  const editor = useEditor({
    extensions: [
      StarterKit.configure({
        // 骨架里用不到标题（模板字段名本身就是标题，由 fillHeading 承担），
        // 关掉可以避免用户敲 `#` 造出一个和字段标题抢视觉的标题。
        heading: false,
        // 引用块同理：模板里没有这个东西，用户以为能写，导出时会被压平。
        blockquote: false,
      }),
      Image.configure({
        // 图片存 data URL：填表场景里用户是从本机选的图，没有对象存储可传，
        // 而且他要能把「填好的数据」整包带走（JSON 里带图）。
        allowBase64: true,
        inline: false,
      }),
      FillHeading,
    ],
    content: toDocJSON(skeleton, seedRef.current),
    editorProps: {
      attributes: {
        class: 'df-mf-body',
        spellcheck: 'false',
      },
      /**
       * 粘贴图片必须拦下来自己处理。
       *
       * Tiptap 的 Image 扩展开了 allowBase64 之后，默认会把剪贴板里的图片
       * 原样内联成 base64 —— 一张手机截图（4000px 宽）进去就是几 MB，
       * 渲染时按物理像素算出来两米宽，直接把版面撑破。用户说的「格式绝对不能变」
       * 在这一步最容易破，而且他在编辑器里看不出来（图片被 max-width 缩着显示），
       * 只有导出后才发现。
       *
       * fileToDataUrl 会把最长边压到 1400px，和「点按钮选图」走同一条路径。
       */
      handlePaste(view, event) {
        const items = Array.from(event.clipboardData?.items || []);
        const imgs = items.filter((it) => it.kind === 'file' && it.type.startsWith('image/'));
        if (imgs.length === 0) return false;

        const files = imgs.map((it) => it.getAsFile()).filter((f): f is File => !!f);
        if (files.length === 0) return false;

        void (async () => {
          for (const f of files) {
            try {
              const src = await fileToDataUrl(f);
              const node = view.state.schema.nodes.image.create({ src });
              view.dispatch(view.state.tr.replaceSelectionWith(node));
            } catch (err) {
              Message.error(`粘贴的图片没能处理：${(err as Error).message}`);
            }
          }
        })();
        return true;
      },
    },
    onUpdate: ({ editor: ed }) => {
      setCharCount(ed.state.doc.textContent.length);
      // 用长度 + 末尾标题做键：只有结构真的变了才重算大纲，避免每次按键都遍历
      let n = 0;
      let tail = '';
      ed.state.doc.forEach((node) => {
        if (node.type.name === 'fillHeading') {
          n += 1;
          tail = String(node.attrs.label ?? '');
        }
      });
      setOutlineKey(`${n}|${tail}`);
      emit(ed.getJSON());
    },
    onCreate: ({ editor: ed }) => {
      setCharCount(ed.state.doc.textContent.length);
      emit(ed.getJSON());
    },
  });

  // 把受控操作塞进 storage，NodeView 里的按钮靠它回调
  useEffect(() => {
    if (!editor) return;
    const storage: FillSkeletonStorage = {
      busy,
      onAddImage: (path) => onPickImage?.(path, path.endsWith('截图') || false),
      onOpenChart: (path) => {
        const { data } = extractData(editor.getJSON());
        onOpenChart?.(path, (data as any)?.[path]);
      },
      onRemoveItem: (itemPath, index) => removeItem(editor, itemPath, index),
    };
    (editor.storage as any).fillSkeleton = storage;
  }, [editor, busy, onPickImage, onOpenChart]);

  /** 顶层节点列表（含各自的位置），插入/删除都要用它算坐标。 */
  const tops = useCallback((ed: NonNullable<typeof editor>) => {
    const list: { node: any; pos: number }[] = [];
    let pos = 0;
    ed.state.doc.forEach((node) => {
      list.push({ node, pos });
      pos += node.nodeSize;
    });
    return list;
  }, []);

  /** 找某个循环最后一条之后、下一条同级内容之前的位置。 */
  const posAfterLastItem = useCallback(
    (ed: NonNullable<typeof editor>, loopPath: string) => {
      const list = tops(ed);
      let lastIdx = -1;
      list.forEach((t, i) => {
        if (t.node.type.name === 'fillHeading' && t.node.attrs.slot === 'item' && t.node.attrs.itemPath === loopPath) {
          lastIdx = i;
        }
      });
      if (lastIdx < 0) return null;
      const level = Number(list[lastIdx].node.attrs.level ?? 2);
      let stop = list.length;
      for (let i = lastIdx + 1; i < list.length; i++) {
        const n = list[i].node;
        if (n.type.name === 'fillHeading' && Number(n.attrs.level) <= level) {
          stop = i;
          break;
        }
      }
      return stop < list.length ? list[stop].pos : ed.state.doc.content.size;
    },
    [tops]
  );

  const addItem = useCallback(
    (loopPath: string) => {
      if (!editor) return;
      const loopVar = headingsOf(skeleton).find((h) => h.slot === 'item' && h.itemPath === loopPath)?.loopVar;
      if (!loopVar) return;
      const pos = posAfterLastItem(editor, loopPath);
      if (pos == null) return;

      const list = tops(editor);
      const count = list.filter(
        (t) => t.node.type.name === 'fillHeading' && t.node.attrs.slot === 'item' && t.node.attrs.itemPath === loopPath
      ).length;
      const level = Number(
        list.find(
          (t) => t.node.type.name === 'fillHeading' && t.node.attrs.slot === 'item' && t.node.attrs.itemPath === loopPath
        )?.node.attrs.level ?? 2
      );

      const json = toDocJSON(nodesForNewItem(loopVar, level, count)).content;
      const frag = Fragment.fromArray(json.map((j) => editor.schema.nodeFromJSON(j)));
      const tr = editor.state.tr.insert(pos, frag);
      tr.setMeta(SKELETON_EDIT_META, true);
      editor.view.dispatch(tr.scrollIntoView());
      editor.commands.focus(pos + 1);
    },
    [editor, skeleton, posAfterLastItem, tops]
  );

  const removeItem = useCallback(
    (ed: NonNullable<typeof editor>, itemPath: string, index: number) => {
      const list = tops(ed);
      let start = -1;
      let level = 2;
      list.forEach((t, i) => {
        if (
          t.node.type.name === 'fillHeading' &&
          t.node.attrs.slot === 'item' &&
          t.node.attrs.itemPath === itemPath &&
          Number(t.node.attrs.itemIndex) === index
        ) {
          start = i;
          level = Number(t.node.attrs.level);
        }
      });
      // attrs 里的 itemIndex 只在初始生成时是对的；用户增删之后按出现顺序找
      if (start < 0) {
        let seen = 0;
        list.forEach((t, i) => {
          if (t.node.type.name === 'fillHeading' && t.node.attrs.slot === 'item' && t.node.attrs.itemPath === itemPath) {
            if (seen === index) {
              start = i;
              level = Number(t.node.attrs.level);
            }
            seen++;
          }
        });
      }
      if (start < 0) return;

      let stop = list.length;
      for (let i = start + 1; i < list.length; i++) {
        const n = list[i].node;
        if (n.type.name === 'fillHeading' && Number(n.attrs.level) <= level) {
          stop = i;
          break;
        }
      }
      const from = list[start].pos;
      const to = stop < list.length ? list[stop].pos : ed.state.doc.content.size;
      const tr = ed.state.tr.delete(from, to);
      tr.setMeta(SKELETON_EDIT_META, true);
      ed.view.dispatch(tr);
      Message.success('已删掉这一条');
    },
    [tops]
  );

  // 句柄交给外层：重置文档、插入图片
  useEffect(() => {
    if (!editor) return;
    onReady?.({
      reset: (data) => {
        seedRef.current = data ?? EMPTY;
        const sk = buildSkeleton(variables, seedRef.current);
        editor.commands.setContent(toDocJSON(sk, seedRef.current), { emitUpdate: false });
        setCharCount(editor.state.doc.textContent.length);
        emit(editor.getJSON());
      },
      insertImage: (path, src) => {
        const list = tops(editor);
        let lastIdx = -1;
        let level = 3;
        list.forEach((t, i) => {
          if (t.node.type.name === 'fillHeading' && t.node.attrs.path === path) {
            lastIdx = i;
            level = Number(t.node.attrs.level);
          }
        });
        if (lastIdx < 0) return;
        let stop = list.length;
        for (let i = lastIdx + 1; i < list.length; i++) {
          const n = list[i].node;
          if (n.type.name === 'fillHeading' && Number(n.attrs.level) <= level) {
            stop = i;
            break;
          }
        }
        const at = stop < list.length ? list[stop].pos : editor.state.doc.content.size;
        const img = editor.schema.nodes.image.create({ src });
        editor.view.dispatch(editor.state.tr.insert(at, img));
      },
    });
  }, [editor, onReady, variables, tops, emit]);

  useEffect(() => {
    return () => {
      if (emitRef.current) window.clearTimeout(emitRef.current);
    };
  }, []);

  /** 滚到第 i 个骨架标题。位置每次现算：用户增删条目后序号会变。 */
  const jumpTo = useCallback(
    (i: number) => {
      if (!editor) return;
      let seen = -1;
      let target: number | null = null;
      editor.state.doc.forEach((node, offset) => {
        if (node.type.name !== 'fillHeading') return;
        seen += 1;
        if (seen === i && target == null) target = offset;
      });
      if (target == null) return;
      const dom = editor.view.nodeDOM(target) as HTMLElement | null;
      dom?.scrollIntoView({ block: 'center', behavior: 'smooth' });
    },
    [editor]
  );

  // 大纲从文档实时算：初始骨架在增删条目后就过期了
  const outlineNow = useMemo(() => {
    if (!editor) return [];
    const out: { label: string; level: number }[] = [];
    editor.state.doc.forEach((node) => {
      if (node.type.name === 'fillHeading') {
        out.push({ label: String(node.attrs.label ?? ''), level: Number(node.attrs.level ?? 2) });
      }
    });
    return out;
  }, [editor, outlineKey]);

  const loops = useMemo(() => loopPaths(skeleton), [skeleton]);

  return (
    <div className="df-mf">
      <div className="df-mf-bar">
        <div className="df-mf-bar__title">
          按标题往下写就行，标题是模板定好的，改不了
        </div>
        <div className="df-mf-bar__right">
          {outlineNow.length > 6 && (
            <Select
              size="mini"
              placeholder="跳到字段"
              showSearch
              style={{ width: 150 }}
              onChange={(v) => jumpTo(Number(v))}
              options={outlineNow.map((h, i) => ({
                label: `${h.level >= 3 ? '· ' : ''}${h.label}`,
                value: String(i),
              }))}
            />
          )}
          {loops.map((l) => (
            <Button
              key={l.path}
              size="mini"
              icon={<IconPlus />}
              onClick={() => addItem(l.path)}
              disabled={busy}
            >
              加一条「{l.label}」
            </Button>
          ))}
          <span className="df-mf-bar__count">{charCount} 字</span>
        </div>
      </div>

      <div className="df-mf-paper">
        <EditorContent editor={editor} />
      </div>
    </div>
  );
}

function headingsOf(nodes: SkeletonNode[]): SkeletonHeading[] {
  return nodes.filter((n): n is SkeletonHeading => n.kind === 'heading');
}

export type { ExtractIssue };
