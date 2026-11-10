import { useRef, useState } from 'react';
import { Alert, Button, Checkbox, Drawer, Empty, Message, Spin, Tag } from '@arco-design/web-react';
import { IconCheckCircle, IconDelete, IconRobot } from '@arco-design/web-react/icon';
import { alignTemplate } from '@/api/endpoints';
import type { AlignMatch, AlignResult, TemplateVariable } from '@/types/api';
import { applyAlignMatches, findVar } from '@/utils/alignapply';
import { richHtmlText } from '@/utils/richtext';
import { errorMessage } from '@/utils/format';
import './freewrite.css';

/**
 * 「粘贴整篇，自动对位」。
 *
 * 这不是"AI 帮我写报告"，而是**反向填表**：用户手里已经有一篇写好的内容
 * （多半是 Word 里的旧报告），要把它塞进这份模板。让他照着表单逐格复制粘贴
 * 是最笨的办法，而且一定会漏。
 *
 * 关键设计取舍：
 *
 * - **不落笔，只给建议**。识别结果以"卡片 + 勾选"的形式给他核，确认后才写进
 *   表单。识别错了最坏是多按一次"取消"，而不是生成一份字段错位的正式文档。
 * - **粘贴的原始 HTML 原样送上服务端**。从 Word 粘过来的内容带着
 *   font-family / font-size——那正是服务端判断「这一行是标题还是正文」的依据。
 *   所以这里**不做**富文本字段那套白名单清洗。清洗掉的是识别信号，
 *   结果就是"用得莫名其妙对不准"。
 * - **默认只勾高置信度**（≥0.75）。0.42~0.62 那档是"有可能"，全勾上等于
 *   把判断责任又推回给用户，他得逐条取消。
 */
export interface FreeWritePanelProps {
  visible: boolean;
  templateId: string;
  variables: TemplateVariable[];
  values: Record<string, unknown>;
  onClose: () => void;
  onApply: (next: Record<string, unknown>) => void;
}

/**
 * 默认勾选的分数门槛。低于它、名字又没对上的建议只显示、不预选。
 *
 * 0.7 是照着引擎的刻度定的：模板里靠标签命中是 0.55、靠字段名命中是 0.63，
 * 「这段带图」进图片槽位是 0.70，字体指纹全篇唯一时能到 0.86。取 0.75 会把
 * 纯图段落也挡在外面——而那张截图恰恰是用户最不想手动去点的东西。
 */
const AUTO_CHECK = 0.7;

/**
 * 这一条该不该默认勾上。
 *
 * 不能只看分数。模板里靠"小标题正好是字段名"命中的匹配分数一律在 0.55 上下
 * （字体指纹那类反而能到 0.7），只按 0.75 划线的话，名字完全对得上的七条
 * 一条都不会被勾上——用户每次都得手动勾七下，等于把判断责任又推回给他，
 * 那这个面板就只剩"帮你把内容搬过来"一半的价值。
 * 所以：**名字对得上就直接勾**，分数只是兜底。
 */
function autoCheck(mt: AlignMatch): boolean {
  return Boolean(mt.name_hit) || mt.score >= AUTO_CHECK;
}

function previewOf(mt: AlignMatch): { text: string; src: string } {
  const v = mt.value;
  if (mt.slot_kind === 'image') {
    const src = typeof v === 'string' ? v : String((v as Record<string, unknown>)?.src || '');
    return { text: src ? '一张图片' : '', src };
  }
  if (v && typeof v === 'object') {
    const content = String((v as Record<string, unknown>).content || '');
    return { text: richHtmlText(content).replace(/\s+/g, ' ').trim(), src: '' };
  }
  return { text: String(v ?? '').replace(/\s+/g, ' ').trim(), src: '' };
}

function scoreTag(mt: AlignMatch) {
  // 名字对得上的按"高"显示，哪怕分数是 0.55 —— 标签上写着「中 55%」而它
  // 已经默认勾上了，人会以为自己看错了。
  if (autoCheck(mt)) return <Tag size="small" color="green">高 {Math.round(mt.score * 100)}%</Tag>;
  if (mt.score >= 0.55) return <Tag size="small" color="arcoblue">中 {Math.round(mt.score * 100)}%</Tag>;
  return <Tag size="small" color="gray">低 {Math.round(mt.score * 100)}%</Tag>;
}

export default function FreeWritePanel({
  visible,
  templateId,
  variables,
  values,
  onClose,
  onApply,
}: FreeWritePanelProps) {
  const boxRef = useRef<HTMLDivElement>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<AlignResult | null>(null);
  const [checked, setChecked] = useState<Set<number>>(new Set());
  const [hasContent, setHasContent] = useState(false);

  const slotNameOf = (mt: AlignMatch) =>
    mt.slot_label || (findVar(variables, mt.slot_path)?.name ?? mt.slot);

  const run = async () => {
    const box = boxRef.current;
    const html = box?.innerHTML || '';
    const text = (box?.textContent || '').trim();
    if (!text && !/<img/i.test(html)) {
      Message.warning('先把你写好的内容粘进来');
      return;
    }
    setBusy(true);
    try {
      const r = await alignTemplate(templateId, html, 'html');
      setResult(r);
      setChecked(new Set(r.matches.map((m, i) => (autoCheck(m) ? i : -1)).filter((i) => i >= 0)));
      if (!r.matches.length) {
        Message.warning('一条都对不上，可能是这份模板没有可填写的字段');
      }
    } catch (err) {
      Message.error(errorMessage(err).summary);
    } finally {
      setBusy(false);
    }
  };

  const toggle = (i: number, on: boolean) => {
    setChecked((prev) => {
      const next = new Set(prev);
      if (on) next.add(i);
      else next.delete(i);
      return next;
    });
  };

  const apply = () => {
    if (!result) return;
    const picked = result.matches.filter((_, i) => checked.has(i));
    if (!picked.length) {
      Message.warning('还没有勾选任何一项');
      return;
    }
    const out = applyAlignMatches(values, variables, picked);
    onApply(out.next);
    if (out.skipped.length) {
      Message.warning(
        `已填入 ${out.applied.length} 项，${out.skipped.length} 项没写进去：${out.skipped
          .map((s) => s.reason)
          .slice(0, 2)
          .join('；')}`,
      );
    } else {
      Message.success(`已填入 ${out.applied.length} 项，确认后可以直接生成`);
    }
    onClose();
  };

  const clearAll = () => {
    if (boxRef.current) boxRef.current.innerHTML = '';
    setHasContent(false);
    setResult(null);
    setChecked(new Set());
  };

  const unmatched = result?.unmatched_paragraphs?.length || 0;
  const pickedCount = checked.size;

  const body = (
      <div className="df-fw">
        <Alert
          type="info"
          className="df-fw-tip"
          content={
            <div>
              <div>把写好的内容整篇粘进来（从 Word 直接复制即可，截图也能一起粘）。</div>
              <div className="df-fw-tip-sub">
                系统按「小标题 + 正文」的结构和字体字号去判断每一段该落在模板的哪一处。
                <b>它只给建议，不会直接改你的文档</b>——你确认了才写进表单。
              </div>
            </div>
          }
        />

        <div className="df-fw-editor">
          <div className="df-fw-editor-head">
            <span>粘贴区</span>
            <span className="df-fw-editor-hint">从 Word 复制过来会带上字体信息，识别更准</span>
          </div>
          <div className="df-fw-box-wrap">
            {hasContent ? null : (
              <div className="df-fw-ph">
                在这里粘贴整篇内容，比如：
                <br />
                漏洞详情
                <br />
                在挂号查询接口的参数 id 处发现 SQL 注入……
              </div>
            )}
            <div
              ref={boxRef}
              className="df-fw-box"
              contentEditable
              suppressContentEditableWarning
              spellCheck={false}
              onInput={() => setHasContent(Boolean(boxRef.current?.textContent?.trim()))}
              onPaste={() => {
                // 粘贴后 DOM 更新是在事件之后，等一帧再读
                setTimeout(() => setHasContent(Boolean(boxRef.current?.textContent?.trim())), 0);
              }}
            />
          </div>
          <div className="df-fw-editor-foot">
            <Button size="small" type="text" icon={<IconDelete />} onClick={clearAll}>
              清空
            </Button>
            <Button
              size="small"
              type="primary"
              icon={<IconRobot />}
              loading={busy}
              onClick={run}
            >
              {result ? '重新识别' : '识别一下'}
            </Button>
          </div>
        </div>

        <Spin loading={busy} tip="正在比对模板结构…">
          {result ? (
            result.matches.length ? (
              <div className="df-fw-result">
                <div className="df-fw-result-head">
                  <span>
                    识别出 {result.matches.length} 处，已勾选 {pickedCount} 处
                  </span>
                  {unmatched > 0 ? (
                    <span className="df-fw-result-sub">
                      还有 {unmatched} 段没能对上（多半是模板里没有对应的字段）
                    </span>
                  ) : null}
                </div>
                {result.matches.map((mt, i) => {
                  const pv = previewOf(mt);
                  return (
                    <div className={checked.has(i) ? 'df-fw-item is-on' : 'df-fw-item'} key={`${mt.slot}-${i}`}>
                      <Checkbox checked={checked.has(i)} onChange={(on) => toggle(i, on)} />
                      <div className="df-fw-item-body">
                        <div className="df-fw-item-head">
                          <span className="df-fw-slot">{slotNameOf(mt)}</span>
                          {scoreTag(mt)}
                          {mt.why?.length ? (
                            <span className="df-fw-why">{mt.why.join('；')}</span>
                          ) : null}
                        </div>
                        <div className="df-fw-value">
                          {pv.src ? (
                            <img className="df-fw-thumb" src={pv.src} alt="" />
                          ) : (
                            <span className={pv.text ? undefined : 'df-fw-empty'}>
                              {pv.text ? (pv.text.length > 140 ? `${pv.text.slice(0, 140)}…` : pv.text) : '（空）'}
                            </span>
                          )}
                        </div>
                      </div>
                    </div>
                  );
                })}
              </div>
            ) : (
              <Empty description="一条都没对上，请检查这份模板有没有可填写的字段" />
            )
          ) : null}
        </Spin>
      </div>
  );

  return (
    <Drawer
      width={860}
      title="粘贴整篇，自动对位"
      visible={visible}
      onCancel={onClose}
      footer={
        <div className="df-fw-actions">
          <Button onClick={onClose}>取消</Button>
          <Button
            type="primary"
            icon={<IconCheckCircle />}
            disabled={!result || pickedCount === 0}
            onClick={apply}
          >
            填入表单（{pickedCount} 项）
          </Button>
        </div>
      }
    >
      {body}
    </Drawer>
  );
}
