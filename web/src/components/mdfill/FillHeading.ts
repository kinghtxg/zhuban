import { Node, mergeAttributes } from '@tiptap/core';
import { Plugin, PluginKey } from '@tiptap/pm/state';
import { ReactNodeViewRenderer } from '@tiptap/react';
import { FillHeadingView } from './FillHeadingView';

/**
 * 骨架标题节点。
 *
 * 这是一个 **atom** 节点（没有 content）：用户在界面上看得见、点不进去、
 * 改不了字。这是「模板字段名不许被用户改掉」的最省事也最可靠的实现——
 * 如果做成普通 heading 再靠事务拦截去禁止改字，就得在每次按键后 diff 前后
 * 文档、判断哪个字变了、把变化退回去，而中文输入法的组合输入会让这个过程
 * 频繁误判（用户打「高」时先出现拼音，拦截器会把它当成改名）。
 *
 * 用户能编辑的只有标题下面的正文段落，那才是他该填的东西。
 *
 * `isolating: true` 让删除时不会把前后两个字段的正文合并成一个——没有它，
 * 光标在「编号」下方按退格键，会把「编号」这个标题干掉，然后「漏洞 1」和
 * 「编号」的值串成一段，而用户以为自己只是删了个空行。
 */
export const FillHeading = Node.create({
  name: 'fillHeading',
  group: 'block',
  atom: true,
  selectable: true,
  draggable: false,
  isolating: true,

  addAttributes() {
    return {
      path: { default: '' },
      label: { default: '' },
      level: { default: 2 },
      slot: { default: 'scalar' },
      hint: { default: null },
      required: { default: false },
      enum: { default: null },
      paraField: { default: null },
      imageField: { default: null },
      itemPath: { default: null },
      itemIndex: { default: null },
      // chart 专用
      chartKind: { default: null },
      chartTitle: { default: null },
      seriesCount: { default: null },
      pointCount: { default: null },
    };
  },

  parseHTML() {
    return [{ tag: 'div[data-fill-heading]' }];
  },

  renderHTML({ HTMLAttributes }) {
    return ['div', mergeAttributes(HTMLAttributes, { 'data-fill-heading': '' })];
  },

  addNodeView() {
    return ReactNodeViewRenderer(FillHeadingView, {
      /**
       * 让标题条里的按钮自己接管鼠标事件。
       *
       * 不加这个，点「高」什么都不会发生：atom 节点在 ProseMirror 的内容区里，
       * 鼠标事件先被编辑器视图处理，React 的 onClick 收不到（实测 applyEnum
       * 调用次数为 0，连按钮自己的 mousedown 都没走到）。
       * 只放行 BUTTON，标题条本身的点击/选中行为不受影响。
       */
      stopEvent: ({ event }) => {
        const t = event.target as HTMLElement | null;
        return !!t && (t.tagName === 'BUTTON' || !!t.closest?.('button'));
      },
    });
  },

  addProseMirrorPlugins() {
    return [skeletonGuard()];
  },
});

/** 事务里表示「这次改动是骨架自己发起的」的 meta 键。 */
export const SKELETON_EDIT_META = 'fillSkeletonEdit';

/**
 * 骨架守护：任何让骨架标题消失的事务一律拒绝。
 *
 * 判据是**按 path 计数**而不是「标题总数」：用户删掉「漏洞 3」再重新加一条，
 * 总数不变，但 `漏洞列表[2]` 的正文其实已经跟着没了。按 path 计数能抓住
 * 「总量相同但结构变了」这种情况。
 *
 * 例外：带 `fillSkeletonEdit` meta 的事务放行。增删条目必须走这条通路——
 * 那不是「用户误删」，是产品提供的操作。
 */
function skeletonGuard() {
  return new Plugin({
    key: new PluginKey('fillSkeletonGuard'),
    filterTransaction(tr, state) {
      if (!tr.docChanged) return true;
      if (tr.getMeta(SKELETON_EDIT_META)) return true;

      const before = countByPath(state.doc);
      const after = countByPath(tr.doc);
      for (const [path, n] of before) {
        if ((after.get(path) ?? 0) < n) return false;
      }
      // 标题文字/级别被改也拦：atom 节点理论上改不了，但粘贴一段
      // 同名 HTML 进来是能改的（parseHTML 会构造新节点）。
      if (!sameLabels(state.doc, tr.doc)) return false;
      return true;
    },
  });
}

function countByPath(doc: any): Map<string, number> {
  const m = new Map<string, number>();
  doc.forEach((node: any) => {
    if (node.type.name !== 'fillHeading') return;
    const p = String(node.attrs?.path ?? '');
    m.set(p, (m.get(p) ?? 0) + 1);
  });
  return m;
}

function sameLabels(a: any, b: any): boolean {
  const la: string[] = [];
  const lb: string[] = [];
  a.forEach((n: any) => {
    if (n.type.name === 'fillHeading') la.push(`${n.attrs?.path}|${n.attrs?.label}|${n.attrs?.level}`);
  });
  b.forEach((n: any) => {
    if (n.type.name === 'fillHeading') lb.push(`${n.attrs?.path}|${n.attrs?.label}|${n.attrs?.level}`);
  });
  if (la.length !== lb.length) return false;
  return la.every((x, i) => x === lb[i]);
}
