import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Alert,
  Button,
  Drawer,
  Input,
  Message,
  Modal,
  Space,
  Tabs,
  Tag,
  Tooltip,
} from '@arco-design/web-react';
import {
  applyEditorModel,
  getEditorModel,
  getTemplate,
  listEditorVersions,
  rollbackEditorVersion,
  saveEditorVersion,
} from '@/api/endpoints';
import type { TemplateVariable } from '@/types/api';
import type { EditBlock, EditRow, EditorModelResp, EditorVersion } from '@/types/editor';
import { errorMessage, fmtTime } from '@/utils/format';
import {
  clearDraft,
  kindName,
  loadDraft,
  nextNewId,
  serializeForApply,
  writeDraft,
} from './model';
import { cacheSealName } from './signsealApi';
import { fitBox, mmToPx, normalizePage } from './layout';
import VariablesPanel from './VariablesPanel';

/**
 * 把一段 jinja 标签接到块的文字末尾。
 *
 * 只对段落/标题有意义：其他块（表格、图表、独立占位符）没有「文字」这个口。
 * 返回原对象表示没改动，调用方可以据此判断插入是否真的发生了。
 */
function appendToken(block: EditBlock, token: string): EditBlock {
  if (block.type === 'paragraph' || block.type === 'heading') {
    // runs 是旧文字的切片，追加后必须丢掉，否则画布还显示追加前的内容
    return { ...block, text: (block.text ?? '') + token, runs: undefined };
  }
  return block;
}

type SaveState = 'idle' | 'dirty' | 'rendering' | 'saved' | 'failed';

/** 变量类型的中文名（降级视图用，与变量页口径一致） */
const varKindLabel: Record<string, string> = {
  scalar: '文本',
  loop: '表格循环',
  condition: '条件',
  image: '图片',
  qrcode: '二维码',
  richtext: '富文本',
};

/** 把变量的 sample 压成一行短文本，给降级视图里的「示例」列用 */
function samplePreview(v: unknown): string {
  if (v === null || v === undefined || v === '') return '';
  if (typeof v === 'string') return v.length > 60 ? `${v.slice(0, 60)}…` : v;
  if (typeof v === 'number' || typeof v === 'boolean') return String(v);
  if (Array.isArray(v)) return `[${v.length} 项]`;
  if (typeof v === 'object') return '{…}';
  return String(v);
}

/**
 * 画布自适应缩放。
 * 纸张宽度不再写死 794px —— 它由文档自己的 pgSz 决定（见 normalizePage）；
 * 三栏布局下可用宽度可能小于纸宽（曾导致表格被裁、右侧列看不见）。
 * 用 ResizeObserver 跟随容器宽度算出缩放比，保证纸张始终完整可见。
 * 用 zoom 而不是 transform:scale —— zoom 会参与布局计算，不会在下方留出空白区域。
 */
function usePaperFit(paperWidth: number) {
  const ref = useRef<HTMLDivElement>(null);
  const [scale, setScale] = useState(1);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const calc = () => {
      const avail = el.clientWidth - 32; // 画布左右各留 16px 呼吸
      if (avail <= 0) return;
      setScale(Math.min(1, Math.max(0.4, avail / paperWidth)));
    };
    calc();
    const ro = new ResizeObserver(calc);
    ro.observe(el);
    return () => ro.disconnect();
  }, [paperWidth]);
  return { ref, scale };
}

export default function EditorPage() {
  const { templateId = '' } = useParams();
  const navigate = useNavigate();

  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [model, setModel] = useState<EditorModelResp | null>(null);
  // 结构解析失败后的降级视图：变量清单 + 示例数据。
  // 这两样在版本落库时就已经独立存好（current_version.variables / sample_json，
  // 由导入时的扫描阶段写入 JSONB），**不依赖 document.xml 的块级解析**。
  // 所以"在线排版不可用"不等于"这个模板不能用了" —— 变量照样能看、能改、能生成。
  const [fallbackVars, setFallbackVars] = useState<TemplateVariable[] | null>(null);
  const [fallbackSample, setFallbackSample] = useState<Record<string, unknown> | null>(null);
  const [fallbackLoading, setFallbackLoading] = useState(false);
  const [blocks, setBlocks] = useState<EditBlock[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [saveState, setSaveState] = useState<SaveState>('idle');
  const [previewPages, setPreviewPages] = useState<{ page: number; url: string }[]>([]);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [previewLoading, setPreviewLoading] = useState(false);

  // 预览面板默认收起：它依赖 LibreOffice 出页面图，未安装时整块是空白，
  // 却仍占 380px 宽，挤压画布。需要时点顶栏「预览」展开。
  const [previewOpen, setPreviewOpen] = useState(false);
  // 页面几何：纸张尺寸与页边距都来自文档自己的 sectPr，缺失时按 A4 兜底。
  const pageGeom = useMemo(() => normalizePage(model?.page), [model?.page]);
  const paperWidthPx = mmToPx(pageGeom.width_mm);
  const { ref: canvasRef, scale: paperScale } = usePaperFit(paperWidthPx);

  /** 纸张样式：宽度/最小高度/四边留白全部按 mm 换算，不再写死 794px。 */
  const paperStyle = useMemo<CSSProperties>(() => ({
    zoom: paperScale,
    width: paperWidthPx,
    minHeight: mmToPx(pageGeom.height_mm),
    paddingTop: mmToPx(pageGeom.margin_top_mm),
    paddingRight: mmToPx(pageGeom.margin_right_mm),
    paddingBottom: mmToPx(pageGeom.margin_bottom_mm),
    paddingLeft: mmToPx(pageGeom.margin_left_mm),
  }), [paperScale, paperWidthPx, pageGeom]);

  const [versions, setVersions] = useState<EditorVersion[]>([]);
  const [showVersions, setShowVersions] = useState(false);
  const [showSave, setShowSave] = useState(false);
  const [summary, setSummary] = useState('');
  const [conflict, setConflict] = useState<{ current_version?: string; current_version_id?: string } | null>(null);
  const [draftPrompt, setDraftPrompt] = useState<{ count: number } | null>(null);
  const [exportBusy, setExportBusy] = useState<'docx' | 'pdf' | null>(null);
  const [opsOpen, setOpsOpen] = useState(false);
  const [picker, setPicker] = useState<null | 'signature' | 'seal'>(null);
  // 打开签章选择器时记住锚点块：PlaceholderPopover 关闭会清空 selectedId，插入需用锚点定位
  const [pickerAnchorId, setPickerAnchorId] = useState<string | null>(null);
  // 最近点中的单元格。作用有两个：显示选中框；作为「插入签名/印章」的落点。
  // 没有它，签章只能插到表格外面（新起一段），那才是最伤版面的插法。
  const [focusedCell, setFocusedCell] = useState<{ tableId: string; rowIdx: number; cellIdx: number } | null>(null);

  const applyTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const draftTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  const blocksRef = useRef<EditBlock[]>([]);
  blocksRef.current = blocks;
  const versionIdRef = useRef<string>('');

  // -------------------------------------------------------------------------
  // 拉取模型
  // -------------------------------------------------------------------------
  const loadModel = useCallback(
    async (useDraft: boolean) => {
      setLoading(true);
      setLoadError(null);
      try {
        const resp = await getEditorModel(templateId);
        setModel(resp);
        versionIdRef.current = resp.version_id;
        setBlocks(resp.blocks);
        setPreviewPages([]);
        setPreviewError(null);
        // 草稿恢复提示
        if (!useDraft) {
          const d = loadDraft(templateId, resp.version_id);
          if (d && JSON.stringify(d.blocks) !== JSON.stringify(resp.blocks)) {
            setDraftPrompt({ count: d.blocks.length });
          }
        }
      } catch (e) {
        setLoadError(errorMessage(e).summary);
      } finally {
        setLoading(false);
      }
    },
    [templateId],
  );

  useEffect(() => {
    loadModel(false);
  }, [loadModel]);

  // 结构解析失败时补拉变量清单，用来渲染「降级视图」而不是一个死胡同。
  useEffect(() => {
    if (!loadError) return;
    let alive = true;
    setFallbackLoading(true);
    getTemplate(templateId)
      .then((t) => {
        if (!alive) return;
        setFallbackVars(t.current_version?.variables ?? []);
        setFallbackSample(t.current_version?.sample_json ?? null);
      })
      .catch(() => {
        if (alive) setFallbackVars(null);
      })
      .finally(() => {
        if (alive) setFallbackLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [loadError, templateId]);

  // -------------------------------------------------------------------------
  // 防抖 apply（1.5s）→ 实时预览；AbortController 取消旧请求
  // -------------------------------------------------------------------------
  const runApply = useCallback(
    async (nextBlocks: EditBlock[]) => {
      if (abortRef.current) abortRef.current.abort();
      const ac = new AbortController();
      abortRef.current = ac;
      setPreviewLoading(true);
      setPreviewError(null);
      try {
        const resp = await applyEditorModel(templateId, serializeForApply(nextBlocks), versionIdRef.current, ac.signal);
        setPreviewPages(resp.pages || []);
        setSaveState('saved');
      } catch (e) {
        if ((e as Error).name === 'AbortError') return;
        const em = errorMessage(e);
        setPreviewError(em.summary);
        setSaveState('failed');
      } finally {
        if (!ac.signal.aborted) setPreviewLoading(false);
      }
    },
    [templateId],
  );

  const scheduleApply = useCallback(() => {
    setSaveState('dirty');
    if (applyTimer.current) clearTimeout(applyTimer.current);
    applyTimer.current = setTimeout(() => {
      setSaveState('rendering');
      runApply(blocksRef.current);
    }, 1500);
    // 草稿防抖 60s 写一次
    if (draftTimer.current) clearTimeout(draftTimer.current);
    draftTimer.current = setTimeout(() => {
      writeDraft(templateId, versionIdRef.current, blocksRef.current);
    }, 60000);
  }, [templateId, runApply]);

  // -------------------------------------------------------------------------
  // 块编辑操作（不可变更新）
  // -------------------------------------------------------------------------
  const mutate = useCallback(
    (fn: (bs: EditBlock[]) => EditBlock[]) => {
      setBlocks((prev) => fn(prev));
      scheduleApply();
    },
    [scheduleApply],
  );

  const updateText = useCallback(
    (id: string, text: string) => {
      // 必须把 runs 清掉：runs 是「按原文字符切好的带格式片段」，文字一旦改动，
      // 它们的拼接结果就和 text 对不上，画布会继续显示旧文字。
      mutate((bs) => bs.map((b) => (b.id === id ? ({ ...b, text, runs: undefined } as EditBlock) : b)));
    },
    [mutate],
  );

  const updateCellText = useCallback(
    (tableId: string, rowIdx: number, cellIdx: number, text: string) => {
      mutate((bs) =>
        bs.map((b) => {
          if (b.id !== tableId || b.type !== 'table') return b;
          const rows = b.rows.map((r, ri) => {
            if (ri !== rowIdx) return r;
            const cells = r.cells.map((c, ci) => {
              if (ci !== cellIdx) return c;
              // 同 updateText：文字改了，旧的 runs 切片就失效了
              return { ...c, blocks: c.blocks.map((cb, bi) => (bi === 0 ? { ...cb, text, runs: undefined } : cb)) };
            });
            return { ...r, cells };
          });
          return { ...b, rows };
        }),
      );
    },
    [mutate],
  );

  const deleteBlock = useCallback(
    (id: string) => {
      mutate((bs) => bs.filter((b) => b.id !== id));
      setSelectedId((s) => (s === id ? null : s));
    },
    [mutate],
  );

  // 在 selected 块前后插入包裹标记（段级循环/条件）
  const wrapMarker = useCallback(
    (kind: 'for' | 'if', a: string, b: string) => {
      if (!selectedId) return;
      const forText = kind === 'for' ? `{%p for ${a} in ${b} %}` : `{%p if ${a} %}`;
      const endText = kind === 'for' ? '{%p endfor %}' : '{%p endif %}';
      mutate((bs) => {
        const idx = bs.findIndex((x) => x.id === selectedId);
        if (idx < 0) return bs;
        const start: EditBlock = { id: nextNewId(), type: 'paragraph', text: forText };
        const end: EditBlock = { id: nextNewId(), type: 'paragraph', text: endText };
        return [...bs.slice(0, idx), start, bs[idx], end, ...bs.slice(idx + 1)];
      });
      Message.success(kind === 'for' ? '已插入段级循环标记' : '已插入段级条件标记');
    },
    [selectedId, mutate],
  );

  // 插入独立占位符段（标量/图片/二维码/富文本）
  const insertPlaceholderParagraph = useCallback(
    (kind: string, varName: string) => {
      if (!selectedId) return;
      const name = kindName(kind, varName);
      const nb: EditBlock =
        kind === 'scalar'
          ? { id: nextNewId(), type: 'paragraph', text: `{{${name}}}` }
          : { id: nextNewId(), type: 'placeholder', kind: kind as 'image' | 'qrcode' | 'rich', name };
      mutate((bs) => {
        const i = bs.findIndex((x) => x.id === selectedId);
        return i < 0 ? [...bs, nb] : [...bs.slice(0, i + 1), nb, ...bs.slice(i + 1)];
      });
      Message.success(`已插入占位符 {{${name}}}`);
    },
    [selectedId, mutate],
  );

  // V3 插入签名/印章：复用 image 占位块，按 id 前缀 + 绑定字段由后端解析注入。
  //
  // V4 改法（用户反馈「插图片把模板格式搞乱了」）：
  //   老实现无论点在哪儿，都新起一个段落放 {{sig_xxx}}。后果有三条——
  //     1. 段落是新加的，正文整体下移一行；
  //     2. 新段落 deepcopy 锚点段落的 pPr，如果锚点是标题/居中的行，签章也跟着变；
  //     3. 尺寸按 60×20 / 40×40 的定值写进 wp:extent，塞进窄格子里必然撑破。
  //   现在改成「行内追加」：把标签接在被选中那段的文字末尾（或点中的单元格里），
  //   段落还是原来那一个，pPr 不动，行高最多因图片而略增 —— 版面结构不变。
  //   尺寸不再由前端拍板：生成时按原图宽高比缩放到版心内（见 render-service
  //   richdata.resolve_context），这里只在画布上给一个同样比例的示意框。
  const insertSealBlock = useCallback(
    (kind: 'signature' | 'seal', item: { id: string; name: string; width_mm?: number; height_mm?: number }) => {
      const isSig = kind === 'signature';
      const token = `{{${item.id}}}`; // 即 {{sig_xxx}} / {{seal_xxx}}
      cacheSealName(kind, item.id, item.name);

      // 情形一：点中过单元格 —— 写进该格最后一段（没有段落才新建一个）
      if (focusedCell) {
        mutate((bs) =>
          bs.map((b) => {
            if (b.id !== focusedCell.tableId || b.type !== 'table') return b;
            const rows = b.rows.map((r, ri) => {
              if (ri !== focusedCell.rowIdx) return r;
              const cells = r.cells.map((c, ci) => {
                if (ci !== focusedCell.cellIdx) return c;
                const last = c.blocks[c.blocks.length - 1];
                if (last && last.type === 'paragraph') {
                  const blocks = [
                    ...c.blocks.slice(0, -1),
                    // runs 是旧文字的切片，追加后必须丢弃，否则画布还显示旧内容
                    { ...last, text: (last.text ?? '') + token, runs: undefined },
                  ];
                  return { ...c, blocks };
                }
                return { ...c, blocks: [...c.blocks, { id: nextNewId(), type: 'paragraph' as const, text: token }] };
              });
              return { ...r, cells };
            });
            return { ...b, rows };
          }),
        );
        Message.success(`已插入${isSig ? '签名' : '印章'}：${item.name}（已放入所选单元格）`);
        setPicker(null);
        setPickerAnchorId(null);
        return;
      }

      // 情形二：选中的是段落/标题 —— 直接接在它末尾
      const anchorId = selectedId || pickerAnchorId;
      const target = blocksRef.current.find((x) => x.id === anchorId);
      if (target && (target.type === 'paragraph' || target.type === 'heading')) {
        mutate((bs) => bs.map((b) => (b.id === anchorId ? appendToken(b, token) : b)));
        Message.success(`已插入${isSig ? '签名' : '印章'}：${item.name}（接在所选段落末尾）`);
        setPicker(null);
        setPickerAnchorId(null);
        return;
      }

      // 情形三：没有可用落点（或选中的是表格/图表等块）—— 退化成新段落，
      // 但仍要按比例给出示意尺寸，避免用户以为它会很大。
      const fitted = fitBox(item.width_mm, item.height_mm, 60, isSig ? 20 : 40);
      const nb: EditBlock = {
        id: nextNewId(),
        type: 'placeholder',
        kind: 'image',
        name: item.id,
        width_mm: fitted.w,
        height_mm: fitted.h,
        ...(isSig ? { signature_id: item.id } : { seal_id: item.id }),
      };
      mutate((bs) => {
        const i = bs.findIndex((x) => x.id === anchorId);
        return i < 0 ? [...bs, nb] : [...bs.slice(0, i + 1), nb, ...bs.slice(i + 1)];
      });
      Message.success(`已插入${isSig ? '签名' : '印章'}：${item.name}`);
      setPicker(null);
      setPickerAnchorId(null);
    },
    [selectedId, pickerAnchorId, focusedCell, mutate],
  );

  /**
   * 点中单元格：记下锚点，供「插入签名/印章」决定落点。
   * 点表格以外的地方不清空它 —— 用户常见动作是「点一下格子，再去右侧点签章」，
   * 中途点到别处就丢锚点会让插入又退回新起一段。
   */
  const handleCellFocus = useCallback((tableId: string, rowIdx: number, cellIdx: number) => {
    setFocusedCell({ tableId, rowIdx, cellIdx });
  }, []);

  // 表格行操作
  const addRow = useCallback(
    (tableId: string, rowIdx: number, where: 'above' | 'below') => {
      mutate((bs) =>
        bs.map((b) => {
          if (b.id !== tableId || b.type !== 'table') return b;
          const tpl = b.rows[rowIdx];
          const newRow: EditRow = {
            id: nextNewId('newtr'),
            // 复制上一行的格结构做新行：文字清空，runs 同时清掉
            // （runs 里带着旧文字，留着会让新行显示上一行的内容）
            cells: tpl.cells.map((c) => ({ blocks: c.blocks.map((cb) => ({ ...cb, text: '', runs: undefined })) })),
          };
          const rows = [...b.rows];
          rows.splice(where === 'above' ? rowIdx : rowIdx + 1, 0, newRow);
          return { ...b, rows };
        }),
      );
    },
    [mutate],
  );

  const removeRow = useCallback(
    (tableId: string, rowIdx: number) => {
      mutate((bs) =>
        bs.map((b) => {
          if (b.id !== tableId || b.type !== 'table') return b;
          return { ...b, rows: b.rows.filter((_, i) => i !== rowIdx) };
        }),
      );
    },
    [mutate],
  );

  const setLoopRow = useCallback(
    (tableId: string, rowIdx: number, loopRow: boolean, loop?: { var: string; iterable: string }) => {
      mutate((bs) =>
        bs.map((b) => {
          if (b.id !== tableId || b.type !== 'table') return b;
          const rows = b.rows.map((r, ri) => (ri === rowIdx ? { ...r, loop_row: loopRow, loop: loopRow ? loop : undefined } : r));
          return { ...b, rows };
        }),
      );
    },
    [mutate],
  );

  // -------------------------------------------------------------------------
  // 版本 / 保存 / 回滚 / 导出
  // -------------------------------------------------------------------------
  const openVersions = useCallback(async () => {
    setShowVersions(true);
    try {
      const r = await listEditorVersions(templateId);
      setVersions(r.items || []);
    } catch {
      /* 抽屉内空态 */
    }
  }, [templateId]);

  const doSave = useCallback(async () => {
    try {
      await saveEditorVersion(templateId, serializeForApply(blocksRef.current), versionIdRef.current, summary.trim());
      Message.success('已保存为新版本');
      setShowSave(false);
      setSummary('');
      clearDraft(templateId, versionIdRef.current);
      await loadModel(true);
      setVersions([]);
    } catch (e) {
      const em = errorMessage(e);
      const status = (e as { status?: number }).status;
      if (status === 409) {
        setConflict({
          current_version: (e as { current_version?: string }).current_version,
          current_version_id: (e as { current_version_id?: string }).current_version_id,
        });
      } else {
        Message.error(em.summary);
      }
    }
  }, [templateId, summary, loadModel]);

  const doRollback = useCallback(
    async (vid: string) => {
      Modal.confirm({
        title: '回滚到此版本？',
        content: '将把该历史版本复制为一个新版本并激活，历史不丢、版本号递增。',
        okText: '回滚到此版',
        onOk: async () => {
          try {
            await rollbackEditorVersion(templateId, vid);
            Message.success('已回滚并生成新版本');
            clearDraft(templateId, versionIdRef.current);
            await loadModel(true);
            setVersions([]);
          } catch (e) {
            Message.error(errorMessage(e).summary);
          }
        },
      });
    },
    [templateId, loadModel],
  );

  const doExport = useCallback(
    (format: 'docx' | 'pdf') => {
      // 模型未加载完不提交，避免空数组被后端判为非法 model
      if (!blocksRef.current || blocksRef.current.length === 0) {
        Message.warning('模板结构尚未加载完成，请稍候再试');
        return;
      }
      setExportBusy(format);
      Message.loading({ content: '正在生成文件，完成后浏览器将自动下载…', duration: 2.5 })
      // 隐藏表单 POST 到隐藏 iframe：
      // 旧路线 fetch→blob→a.click() 后同步 revokeObjectURL，会在浏览器真正开始读取
      // blob（PDF 渲染约 1.7s）之前取消下载；且 await 后丢失用户手势，连续第二个
      // 下载被浏览器拦截。表单提交是独立用户手势，服务端 Content-Disposition
      // attachment 直接落盘不跳转。不改变 multipart 字段 model/format 契约。
      // model 必须与 apply/save 一致地包成 { blocks: [...] }，裸数组会被渲染端拒绝。
      const modelJson = JSON.stringify({ blocks: serializeForApply(blocksRef.current) });
      const iframe = document.createElement('iframe');
      iframe.name = 'docforge-export-' + format + '-' + Date.now();
      iframe.style.cssText = 'position:absolute;width:0;height:0;border:0;visibility:hidden;';
      document.body.appendChild(iframe);

      const form = document.createElement('form');
      form.method = 'POST';
      form.action = `/api/v1/templates/${templateId}/editor/export`;
      form.target = iframe.name;
      form.enctype = 'multipart/form-data';
      const field = (k: string, v: string) => {
        const i = document.createElement('input');
        i.type = 'hidden';
        i.name = k;
        i.value = v;
        form.appendChild(i);
      };
      field('model', modelJson);
      field('format', format);
      document.body.appendChild(form);
      form.submit();

      const settleMs = format === 'pdf' ? 6000 : 3000;
      const start = Date.now();
      const timer = window.setInterval(() => {
        let bodyText = '';
        try {
          bodyText = iframe.contentDocument?.body?.textContent?.trim() || '';
        } catch {
          bodyText = '';
        }
        if (bodyText) {
          // 渲染失败：错误 JSON 被 iframe 加载为正文（同源可读）
          window.clearInterval(timer);
          const raw = bodyText;
          let msg = raw;
          try {
            const j = JSON.parse(raw);
            msg = j.message || j.error || j.detail || raw;
          } catch {
            /* keep raw text */
          }
          if (/未登录|登录已|401|unauthor|登录已失效/i.test(msg)) {
            Message.error('登录已失效，请重新登录后重试');
          } else {
            Message.error('导出失败：' + String(msg).slice(0, 120));
          }
          cleanup();
          setExportBusy(null);
          return;
        }
        if (Date.now() - start > settleMs) {
          window.clearInterval(timer);
          cleanup();
          Message.success('导出完成，已开始下载');
          setExportBusy(null);
        }
      }, 400);

      function cleanup() {
        // 至少 60s 后再移除 iframe/form：过早移除可能打断慢响应（pdf 3~10s）的下载提交
        window.setTimeout(() => {
          form.remove();
          iframe.remove();
        }, 60000);
      }
    },
    [templateId],
  );

  // Ctrl+S 存为版本；Esc 取消选中
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
        e.preventDefault();
        setShowSave(true);
      }
      if (e.key === 'Escape') setSelectedId(null);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  const saveLabel: Record<SaveState, string> = {
    idle: '未修改',
    dirty: '待渲染',
    rendering: '渲染中…',
    saved: '已渲染',
    failed: '渲染失败',
  };

  const selectedBlock = useMemo(
    () => blocks.find((b) => b.id === selectedId) || null,
    [blocks, selectedId],
  );

  // -------------------------------------------------------------------------
  // 渲染
  // -------------------------------------------------------------------------
  if (loading) {
    return (
      <div className="df-editor df-editor-empty">
        <div style={{ textAlign: 'center' }}>
          <div style={{ fontSize: 15, fontWeight: 600, marginBottom: 8 }}>正在解析模板结构…</div>
          <div style={{ color: 'var(--app-text-caption)', fontSize: 13 }}>大模板解析可能需要数秒</div>
        </div>
      </div>
    );
  }

  if (loadError || !model) {
    // 这一段曾经写的是「该模板含暂不支持的元素，已以只读模式打开。请在 Word 中处理后重新上传。」
    // 三处都不实：
    //   1) 走到这里说明结构解析**整个失败**了，不是"部分元素不支持"——
    //      部分不支持走的是 blocks 里的 unsupported 块 + 顶部提示，编辑器照常打开；
    //   2) 根本没有只读模式，页面就没渲染出来；
    //   3) 真正的原因（loadError）明明拿到了却不显示，用户只能猜。
    // 结果就是用户被引导去 Word 里删东西，删了几轮也没用。
    // 现在：说清楚失败在哪一步、影响哪一块、还有什么照常能用，并把变量清单摊开。
    return (
      <div className="df-editor" style={{ padding: '32px 40px', overflow: 'auto' }}>
        <Alert
          type="warning"
          content="在线排版不可用（模板结构解析失败），但变量与文档生成不受影响"
          style={{ marginBottom: 16 }}
        />
        <div style={{ color: 'var(--app-text-body)', lineHeight: 1.9, marginBottom: 16 }}>
          「在线排版」只是模板生命周期里的一个可选环节。这次失败的是
          <b>把 docx 解析成可编辑块</b>这一步，它和下面两件事互不依赖：
          <ul style={{ margin: '8px 0 8px 20px', padding: 0 }}>
            <li>变量清单是导入时<b>独立扫描</b>后落库的，完整可用，见下方列表；</li>
            <li>填数据生成 docx / PDF 走的是<b>占位符替换</b>，不读这里的结构解析。</li>
          </ul>
          {loadError ? (
            <>
              失败原因：<code
                style={{
                  background: 'var(--color-fill-2)',
                  padding: '2px 8px',
                  borderRadius: 4,
                  fontSize: 13,
                  wordBreak: 'break-all',
                }}
              >
                {loadError}
              </code>
            </>
          ) : null}
        </div>

        <Space style={{ marginBottom: 20 }}>
          <Button type="primary" onClick={() => loadModel(false)}>重试解析</Button>
          <Button onClick={() => navigate(`/templates/${templateId}/variables`)}>变量与数据</Button>
          <Button onClick={() => navigate(`/templates/${templateId}/playground`)}>直接生成文档</Button>
          <Button onClick={() => navigate(`/templates/${templateId}`)}>返回模板详情</Button>
        </Space>

        {/* 降级视图：变量清单 */}
        <div className="df-fallback-panel">
          <div className="df-fallback-head">
            变量清单
            {fallbackLoading ? (
              <span className="df-fallback-sub">加载中…</span>
            ) : fallbackVars ? (
              <span className="df-fallback-sub">共 {fallbackVars.length} 个</span>
            ) : (
              <span className="df-fallback-sub">不可用</span>
            )}
          </div>
          {fallbackVars && fallbackVars.length > 0 ? (
            <div className="df-fallback-table">
              {fallbackVars.map((v) => (
                <div className="df-fallback-row" key={v.path || v.name}>
                  <span className="df-fallback-name" title={v.path || v.name}>
                    {v.name}
                  </span>
                  <span className="df-fallback-kind">{varKindLabel[v.kind] ?? v.kind}</span>
                  <span className="df-fallback-req">{v.required ? '必填' : '选填'}</span>
                  <span className="df-fallback-sample" title={samplePreview(v.sample)}>
                    {samplePreview(v.sample) || '—'}
                  </span>
                </div>
              ))}
            </div>
          ) : (
            !fallbackLoading && (
              <div className="df-fallback-empty">
                该版本没有登记变量清单（可能是纯静态文档，无需替换任何内容）。
              </div>
            )
          )}
        </div>

        {fallbackSample && Object.keys(fallbackSample).length > 0 && (
          <div className="df-fallback-panel">
            <div className="df-fallback-head">示例数据（sample_json）</div>
            <pre className="df-fallback-json">{JSON.stringify(fallbackSample, null, 2)}</pre>
          </div>
        )}
      </div>
    );
  }

  return (
    <div className="df-editor">
      {/* 顶栏 */}
      <div className="df-editor-topbar">
        <button className="back-btn" onClick={() => navigate(`/templates/${templateId}`)}>
          ← 返回
        </button>
        <span className="df-editor-title">{/* 模板名由模板详情带 */}在线模板编辑器</span>
        <span className="df-editor-sub">
          v{model.version_no} · {saveState === 'dirty' || saveState === 'rendering' ? '草稿编辑中' : '草稿'}
        </span>
        <span className="df-editor-spacer" />
        <span className="df-save-state">
          <span className={`df-save-dot ${saveState}`} />
          {saveLabel[saveState]}
        </span>
        <Button size="small" onClick={() => setPreviewOpen((v) => !v)}>
          {previewOpen ? '收起预览' : '预览'}
        </Button>
        <Button size="small" onClick={openVersions}>版本</Button>
        <Button size="small" onClick={() => setShowSave(true)}>存为版本</Button>
        <Tooltip content={exportBusy ? '正在导出…' : '导出当前草稿（不落版本）'}>
          <Button size="small" loading={exportBusy === 'docx'} onClick={() => doExport('docx')}>导出 docx</Button>
        </Tooltip>
        <Tooltip content="导出 PDF（用示例数据渲染）">
          <Button size="small" loading={exportBusy === 'pdf'} onClick={() => doExport('pdf')}>导出 pdf</Button>
        </Tooltip>
      </div>

      <div className="df-editor-body">
        {/* 画布：纸张按容器宽度自适应缩放，避免 A4 固定宽被裁 */}
        <div className="df-editor-canvas-wrap" ref={canvasRef}>
          <div className="df-paper" style={paperStyle}>
            {model.warnings.length > 0 && (
              <Alert
                type="warning"
                style={{ marginBottom: 12 }}
                content={`解析提示：${model.warnings.join('；')}`}
              />
            )}
            {blocks.map((b) => (
              <BlockView
                key={b.id}
                block={b}
                selected={selectedId === b.id}
                onSelect={() => { setSelectedId(b.id); setOpsOpen(false); }}
                onUpdateText={(t) => updateText(b.id, t)}
                onCellText={updateCellText}
                onDelete={() => deleteBlock(b.id)}
                onAddRow={addRow}
                onRemoveRow={removeRow}
                onSetLoopRow={setLoopRow}
                onCellFocus={handleCellFocus}
                focusedCell={focusedCell}
              />
            ))}
            {selectedBlock && (
              <div className="df-block-toolbarbar">
                <Button size="mini" type="primary" onClick={() => setOpsOpen(true)}>插入占位符 / 包裹循环·条件</Button>
                <Button size="mini" status="danger" onClick={() => deleteBlock(selectedBlock.id)}>删除块</Button>
                <span style={{ fontSize: 11, color: 'var(--app-text-caption)', marginLeft: 8 }}>
                  上下移动 / 类型转换 / 列增删：apply 暂不支持，请回 Word 调整
                </span>
              </div>
            )}
          </div>
        </div>

        {/* 实时预览（默认收起，需要时从顶栏打开） */}
        {previewOpen && (
          <div className="df-editor-preview">
            <div className="df-preview-head">
              <span>实时预览</span>
              {previewLoading && <span style={{ fontSize: 11, color: 'rgb(var(--primary-6))' }}>刷新中…</span>}
            </div>
            <div className="df-preview-body">
              {previewError && (
                <Alert
                  type="error"
                  style={{ marginBottom: 10 }}
                  content={previewError}
                  action={<Button size="mini" onClick={() => runApply(blocksRef.current)}>重试</Button>}
                />
              )}
              {!previewError && previewPages.length === 0 && !previewLoading && (
                <div style={{ color: 'var(--app-text-caption)', fontSize: 12, textAlign: 'center', marginTop: 24 }}>
                  编辑后将自动生成预览
                </div>
              )}
              {previewPages.map((p) => (
                <img key={p.page} className="df-preview-page" alt={`第 ${p.page} 页`} src={p.url} />
              ))}
            </div>
          </div>
        )}

        {/* 底部抽屉：变量与资源 / 大纲 */}
        <div className="df-editor-drawer">
          <Tabs
            defaultActiveTab="vars"
            size="small"
            style={{ height: '100%', display: 'flex', flexDirection: 'column' }}
          >
            <Tabs.TabPane key="vars" title="变量与资源" style={{ overflow: 'auto', flex: 1 }}>
              <VariablesPanel templateId={templateId} />
            </Tabs.TabPane>
            <Tabs.TabPane key="outline" title="大纲" style={{ overflow: 'auto', flex: 1 }}>
              <Outline blocks={blocks} onSelect={(id) => setSelectedId(id)} />
            </Tabs.TabPane>
          </Tabs>
        </div>
      </div>

      {/* 占位符 / 包裹标记表单（点浮起工具条打开） */}
      {opsOpen && selectedBlock && (
        <PlaceholderPopover
          block={selectedBlock}
          onClose={() => { setOpsOpen(false); setSelectedId(null); }}
          onWrap={wrapMarker}
          onInsertPlaceholder={insertPlaceholderParagraph}
          onPickSignature={() => { setPickerAnchorId(selectedId); setOpsOpen(false); setPicker('signature'); }}
          onPickSeal={() => { setPickerAnchorId(selectedId); setOpsOpen(false); setPicker('seal'); }}
        />
      )}

      {/* V3 签名/印章选择抽屉 */}
      {picker && (
        <SealPickerModal
          kind={picker}
          onClose={() => { setPicker(null); setPickerAnchorId(null); }}
          onSelect={(item) => insertSealBlock(picker, item)}
        />
      )}

      {/* 版本抽屉 */}
      <Drawer
        title="版本时间线"
        visible={showVersions}
        onCancel={() => setShowVersions(false)}
        width={460}
        unmountOnExit
      >
        {versions.map((v) => (
          <div key={v.id} style={{ padding: '10px 0', borderBottom: '1px solid var(--app-border)' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <span style={{ fontWeight: 700 }}>v{v.version}</span>
              <Tag color={v.is_current ? 'green' : 'gray'}>{v.is_current ? '当前' : ''}</Tag>
            </div>
            <div style={{ fontSize: 12, color: 'var(--app-text-body)', margin: '4px 0' }}>
              来源：{v.source === 'upload' ? '上传' : v.source === 'online_edit' ? '在线编辑' : '回滚'}
              {v.change_summary ? ` · ${v.change_summary}` : ''}
            </div>
            <div style={{ fontSize: 11, color: 'var(--app-text-caption)' }}>{fmtTime(v.created_at)}</div>
            {!v.is_current && (
              <Button size="mini" style={{ marginTop: 6 }} onClick={() => doRollback(v.id)}>回滚到此版</Button>
            )}
          </div>
        ))}
      </Drawer>

      {/* 存为版本弹框 */}
      <Modal
        title="存为新版本"
        visible={showSave}
        onOk={doSave}
        onCancel={() => setShowSave(false)}
        okText="保存为新版本"
      >
        <div style={{ marginBottom: 8, fontSize: 13, color: 'var(--app-text-body)' }}>变更摘要（可选）</div>
        <Input.TextArea rows={3} value={summary} onChange={setSummary} placeholder="例如：调整问题表格列、修改报告抬头" />
      </Modal>

      {/* 409 冲突 */}
      <Modal
        title="模板已被更新"
        visible={!!conflict}
        onCancel={() => setConflict(null)}
        footer={
          <div style={{ textAlign: 'right' }}>
            <Button style={{ marginRight: 8 }} onClick={() => setConflict(null)}>取消</Button>
            <Button
              style={{ marginRight: 8 }}
              onClick={() => {
                // 丢弃本地编辑前先备份草稿
                writeDraft(templateId, conflict?.current_version_id || versionIdRef.current, blocksRef.current);
                setConflict(null);
                loadModel(true);
              }}
            >
              刷新为最新
            </Button>
            <Button type="primary" onClick={() => { setConflict(null); doSave(); }}>
              仍存为新版本
            </Button>
          </div>
        }
      >
        <div style={{ fontSize: 13 }}>
          该模板已被更新为 v{conflict?.current_version || '新版本'}。你可以刷新为最新（本地草稿已备份），或仍将当前编辑存为新版本。
        </div>
      </Modal>

      {/* 草稿恢复 */}
      <Modal
        title="发现未保存的草稿"
        visible={!!draftPrompt}
        onCancel={() => setDraftPrompt(null)}
        footer={
          <div style={{ textAlign: 'right' }}>
            <Button style={{ marginRight: 8 }} onClick={() => setDraftPrompt(null)}>丢弃草稿</Button>
            <Button
              type="primary"
              onClick={() => {
                const d = loadDraft(templateId, versionIdRef.current);
                if (d) setBlocks(d.blocks);
                setDraftPrompt(null);
                scheduleApply();
              }}
            >
              恢复草稿
            </Button>
          </div>
        }
      >
        <div style={{ fontSize: 13, color: 'var(--app-text-body)' }}>
          检测到上次未保存的本地编辑（{draftPrompt?.count ?? 0} 个块）。是否恢复？
        </div>
      </Modal>
    </div>
  );
}

// 大纲
function Outline({ blocks, onSelect }: { blocks: EditBlock[]; onSelect: (id: string) => void }) {
  return (
    <div style={{ padding: 8 }}>
      {blocks.map((b) => {
        let label = '';
        let indent = 0;
        if (b.type === 'heading') { label = b.text || `标题${b.level}`; indent = (b.level - 1) * 12; }
        else if (b.type === 'paragraph') { label = b.text || '空段落'; indent = 12; }
        else if (b.type === 'table') { label = `表格（${b.rows.length} 行）`; indent = 6; }
        else if (b.type === 'placeholder') { label = `占位符 {{${b.name}}}`; indent = 12; }
        else if (b.type === 'unsupported') { label = '不支持元素'; indent = 6; }
        return (
          <div
            key={b.id}
            style={{ padding: '4px 6px', paddingLeft: 12 + indent, fontSize: 12, cursor: 'pointer', borderRadius: 4, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}
            onClick={() => onSelect(b.id)}
            onMouseEnter={(e) => (e.currentTarget.style.background = 'var(--app-bg-hover)')}
            onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
          >
            {label}
          </div>
        );
      })}
    </div>
  );
}

// 单个块视图（含工具条）
import BlockView from './BlockView';
import PlaceholderPopover from './PlaceholderPopover';
import SealPickerModal from './SealPickerModal';
