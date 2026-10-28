import { useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert,
  Button,
  Descriptions,
  Divider,
  Message,
  Radio,
  Space,
  Tag,
} from '@arco-design/web-react';
import { useNavigate, useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
// 编辑器初始化是副作用模块，必须在这里引入（而不是入口）：
// 这样 Monaco 只跟着这个页面按需下载。
import '@/monaco-setup';
import Editor, { type OnMount } from '@monaco-editor/react';
import { getTemplate, listUsableSeals, listUsableSignatures, renderDoc, validateData } from '@/api/endpoints';
import { KIND_META } from '@/constants/enums';
import type { Format, TemplateVariable, ValidateIssue } from '@/types/api';
import { errorMessage } from '@/utils/format';
import VariableTree from '@/components/VariableTree';
import ChartDataDrawer, { type ChartPayload } from '@/components/ChartDataDrawer';
import SignAssetPicker from '@/components/SignAssetPicker';
import PreviewPane from '@/components/PreviewPane';
import EmptyState from '@/components/EmptyState';

/**
 * 从数据里的值取出资源 ID。
 *
 * 签章的值有两种合法形态：
 *   "sig_xxxx"                                        ← 老形态：直接引用资源
 *   {"$sig":"sig_xxxx","with_timestamp":true,...}      ← 参数对象：现场出图（可带签署时间）
 * 只认字符串会让参数对象在界面上显示成「未选择」——用户明明填了，界面说没填。
 */
export function assetIdOf(v: unknown): string {
  if (typeof v === 'string') return v;
  if (v && typeof v === 'object') {
    const o = v as Record<string, unknown>;
    const id = o.$sig ?? o.$seal;
    if (typeof id === 'string') return id;
  }
  return '';
}

/** 值里是否带了现场出图参数（时间/字体/署名）。用来在界面上标一下，别让人忘了自己开过。 */
export function renderParamsOf(v: unknown): string[] {
  if (!v || typeof v !== 'object' || Array.isArray(v)) return [];
  const o = v as Record<string, unknown>;
  const out: string[] = [];
  if (o.with_timestamp || o.timestamp) out.push('带签署时间');
  if (o.font) out.push('指定字体');
  if (o.signer_name) out.push('指定署名');
  if (o.width_mm || o.height_mm) out.push('指定尺寸');
  return out;
}

export default function StudioPage() {
  // 新路由 /templates/:id/playground（参数名 id）；旧路由 /studio/:templateId（参数名 templateId）。
  // 两个都兼容，取到即用，localStorage 草稿 key 也统一用解析后的 id。
  const { templateId: legacyId, id: routeId } = useParams();
  const templateId = legacyId || routeId || '';
  const navigate = useNavigate();

  const { data: tpl, isLoading } = useQuery({
    queryKey: ['template', templateId],
    queryFn: () => getTemplate(templateId),
    enabled: !!templateId,
  });

  const [jsonText, setJsonText] = useState('');
  const [format, setFormat] = useState<Format>('docx');
  const [editTick, setEditTick] = useState(0);
  const [validateResult, setValidateResult] = useState<ValidateIssue[]>([]);
  const [validating, setValidating] = useState(false);
  const [generating, setGenerating] = useState(false);
  // 图表数据编辑器：点「改数据」打开网格，保存后写回 JSON
  const [chartEdit, setChartEdit] = useState<TemplateVariable | null>(null);
  // 选签章抽屉：kind 由变量名前缀决定
  const [assetPick, setAssetPick] = useState<{ v: TemplateVariable; kind: 'signature' | 'seal' } | null>(
    null,
  );
  const monacoRef = useRef<Parameters<OnMount>[0] | null>(null);

  // 草稿：localStorage 按模板存
  useEffect(() => {
    const saved = localStorage.getItem(`df-draft-${templateId}`);
    if (saved && !jsonText) setJsonText(saved);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [templateId]);

  useEffect(() => {
    if (jsonText) localStorage.setItem(`df-draft-${templateId}`, jsonText);
  }, [jsonText, templateId]);

  const parsed = useMemo(() => {
    if (!jsonText.trim()) return { ok: true, value: null as unknown };
    try {
      return { ok: true, value: JSON.parse(jsonText) as unknown };
    } catch (e) {
      return { ok: false, value: null, err: e instanceof Error ? e.message : 'JSON 语法错误' };
    }
  }, [jsonText]);

  const fillSample = () => {
    const s = tpl?.current_version?.sample_json;
    if (!s) {
      Message.warning('当前模板没有示例数据');
      return;
    }
    setJsonText(JSON.stringify(s, null, 2));
    Message.success('已填入示例 JSON，值为占位符「（待填）」，请替换为真实数据');
  };

  const handleMount: OnMount = (editor) => {
    monacoRef.current = editor;
  };

  // 图表变量（chartdata 扫描产物）
  const chartVars = useMemo(
    () => (tpl?.current_version?.variables || []).filter((v) => v.kind === 'chart'),
    [tpl],
  );

  // 签章变量：sig_* / seal_*。值是资源 ID（不是图），必须让人从「我能用的」里选，
  // 否则只能手填 ID —— 而 ID 是不可知的。
  const signVars = useMemo(
    () =>
      (tpl?.current_version?.variables || []).filter(
        (v) => v.name.startsWith('sig_') || v.name.startsWith('seal_'),
      ),
    [tpl],
  );

  // 已知资源 id -> 名字。用来把 JSON 里已有的 ID 显示成人能看懂的名字；
  // 刷新页面后也能显示（授权到期/被删的资源会退化成显示原始 ID）。
  const { data: usableData } = useQuery({
    queryKey: ['usable-assets', 'both'],
    queryFn: async () => {
      const [s, e] = await Promise.all([listUsableSignatures(), listUsableSeals()]);
      const map: Record<string, { name: string; kind: 'signature' | 'seal' }> = {};
      for (const a of s.items) map[a.id] = { name: a.name, kind: 'signature' };
      for (const a of e.items) map[a.id] = { name: a.name, kind: 'seal' };
      return map;
    },
    enabled: signVars.length > 0,
  });
  const assetName = (v: unknown) => {
    const id = assetIdOf(v);
    return id ? usableData?.[id]?.name || id : '';
  };

  /** 把选中的签章 ID 写回数据 JSON（其它键原样保留） */
  const applyAsset = (name: string, assetId: string) => {
    if (!parsed.ok) {
      Message.warning('当前 JSON 有语法错误，请先修正再选择签章');
      return;
    }
    const base = (parsed.value && typeof parsed.value === 'object' ? parsed.value : {}) as Record<
      string,
      unknown
    >;
    const prev = base[name];
    // 「换一个签章」只该换资源，不该顺手把现场出图参数（时间/字体/署名）清掉——
    // 那种丢失用户看不见，生成出来才发现时间没了。
    if (prev && typeof prev === 'object' && !Array.isArray(prev) && (('$sig' in prev) || ('$seal' in prev))) {
      const marker = '$seal' in prev ? '$seal' : '$sig';
      base[name] = { ...(prev as Record<string, unknown>), [marker]: assetId };
    } else {
      base[name] = assetId;
    }
    setJsonText(JSON.stringify(base, null, 2));
  };

  /** 开关「生成时印上签署时间」：在「资源 ID 字符串」和「参数对象」两种形态之间切换。 */
  const toggleTimestamp = (name: string) => {
    if (!parsed.ok) {
      Message.warning('当前 JSON 有语法错误，请先修正再设置');
      return;
    }
    const base = (parsed.value && typeof parsed.value === 'object' ? parsed.value : {}) as Record<
      string,
      unknown
    >;
    const prev = base[name];
    const id = assetIdOf(prev);
    if (!id) {
      Message.warning('先选择签名，再设置是否印时间');
      return;
    }
    const obj: Record<string, unknown> =
      prev && typeof prev === 'object' && !Array.isArray(prev)
        ? { ...(prev as Record<string, unknown>) }
        : { $sig: id };
    const on = Boolean(obj.with_timestamp || obj.timestamp);
    if (on) {
      delete obj.with_timestamp;
      delete obj.timestamp;
      // 只剩 $sig 就退回最朴素的字符串形态，JSON 更干净好读
      const rest = Object.keys(obj).filter((k) => k !== '$sig' && k !== '$seal');
      base[name] = rest.length ? obj : id;
    } else {
      // 时间不在前端算：只声明「用生成那一刻」，由服务端在出图时求值
      base[name] = {
        ...obj,
        $sig: id,
        with_timestamp: true,
        timestamp: { $date: 'now', format: '%Y-%m-%d %H:%M' },
      };
    }
    setJsonText(JSON.stringify(base, null, 2));
  };

  /** 把图表网格的编辑结果写回数据 JSON（其它键原样保留） */
  const applyChart = (name: string, payload: ChartPayload) => {
    if (!parsed.ok) {
      Message.warning('当前 JSON 有语法错误，请先修正再编辑图表');
      return;
    }
    const base = (parsed.value && typeof parsed.value === 'object' ? parsed.value : {}) as Record<
      string,
      unknown
    >;
    base[name] = payload;
    setJsonText(JSON.stringify(base, null, 2));
    setChartEdit(null);
    Message.success(`图表「${name}」数据已写回，可点「校验数据」或直接生成`);
  };

  const runValidate = async () => {
    if (!parsed.ok) {
      Message.warning('JSON 语法错误，请先修正编辑器里的红色波浪线');
      return;
    }
    if (parsed.value === null) {
      Message.warning('请先填入数据');
      return;
    }
    setValidating(true);
    try {
      const r = await validateData({ template_id: templateId, version_id: tpl?.current_version_id, data: parsed.value });
      setValidateResult(r.errors || []);
      if (r.valid) Message.success('校验通过');
      else Message.warning(`校验发现 ${r.errors?.length || 0} 个问题`);
      // 在 Monaco 里标红（按 path 粗略匹配行）
      const model = monacoRef.current?.getModel();
      if (model && window.monaco) {
        const markers = (r.errors || []).map((e) => {
          const line = findLineForPath(model.getValue(), e.path);
          return {
            startLineNumber: line,
            endLineNumber: line,
            startColumn: 1,
            endColumn: 1,
            message: e.message,
            severity: 8, // Error
          };
        });
        window.monaco.editor.setModelMarkers(model, 'docforge', markers);
      }
    } catch (err) {
      Message.error(errorMessage(err).summary);
    } finally {
      setValidating(false);
    }
  };

  const generate = async () => {
    if (!parsed.ok || parsed.value === null) {
      Message.warning('请先填入合法 JSON 数据');
      return;
    }
    setGenerating(true);
    try {
      const r = await renderDoc({
        template_id: templateId,
        version_id: tpl?.current_version_id,
        data: parsed.value,
        format,
      });
      if (r.sync && r.documents && r.documents[0]) {
        const doc = r.documents[0];
        const url = doc.signed_url || doc.download_url;
        if (url) {
          window.open(url, '_blank');
          Message.success('已生成，正在下载');
        }
      } else {
        Message.success('任务已提交，可在「文档生成中心」查看进度与下载');
        navigate('/documents');
      }
    } catch (err) {
      Message.error(errorMessage(err).summary);
    } finally {
      setGenerating(false);
    }
  };

  if (isLoading) return <div className="df-page">加载模板中…</div>;
  if (!tpl) {
    return (
      <div className="df-page">
        <EmptyState
          description="模板不存在或已停用"
          action={<Button onClick={() => navigate('/templates')}>返回模板库</Button>}
        />
      </div>
    );
  }

  return (
    <div>
      {/* 顶栏：format 切换 + 校验 + 生成 */}
      <div style={{ display: 'flex', justifyContent: 'flex-end', alignItems: 'center', padding: '8px 24px 0' }}>
        <Space>
          <Radio.Group type="button" value={format} onChange={(v) => setFormat(v as Format)}>
            <Radio value="docx">docx</Radio>
            <Radio value="pdf">pdf</Radio>
          </Radio.Group>
          <Button onClick={runValidate} loading={validating}>
            校验数据
          </Button>
          <Button type="primary" loading={generating} onClick={generate}>
            生成并下载（{format.toUpperCase()}）
          </Button>
        </Space>
      </div>

      <div className="df-studio">
        {/* 左：模板信息 + 变量树 */}
        <div className="df-studio-col df-studio-col-left">
          <div className="df-studio-col-header">变量清单</div>
          <div className="df-studio-col-body">
            <Descriptions
              column={1}
              size="mini"
              data={[
                { label: '编号', value: tpl.code },
                { label: '版本', value: tpl.current_version?.version },
              ]}
              style={{ marginBottom: 12 }}
            />
            <Button long type="outline" onClick={fillSample} style={{ marginBottom: 12 }}>
              填充示例 JSON
            </Button>
            <Divider style={{ margin: '8px 0' }} />
            <VariableTree variables={tpl.current_version?.variables || []} />

            {/* 图表数据：图表没有 {{}} 文本占位符，必须单独给一个可视化入口，
                否则用户只能手写 JSON 里的 categories/series 数组。
                系列数锁定由抽屉内部保证。 */}
            {chartVars.length > 0 && (
              <>
                <Divider style={{ margin: '12px 0 8px' }} />
                <div className="studio-sub-title">
                  图表数据<span>{chartVars.length}</span>
                </div>
                {chartVars.map((v) => {
                  const cur = parsed.ok && parsed.value && typeof parsed.value === 'object'
                    ? (parsed.value as Record<string, unknown>)[v.name]
                    : undefined;
                  const edited = cur !== undefined;
                  return (
                    <div className="studio-chart-row" key={v.name}>
                      <div className="studio-chart-info">
                        <div className="studio-chart-name" title={v.name}>
                          {v.title || v.name}
                        </div>
                        <div className="studio-chart-meta">
                          {v.chart_kind_name || '图表'} · {v.series_count ?? '?'} 系列 ·{' '}
                          {v.point_count ?? '?'} 点
                          {edited && <Tag size="small" color="green" style={{ marginLeft: 6 }}>已改</Tag>}
                        </div>
                      </div>
                      <Button size="mini" type="outline" onClick={() => setChartEdit(v)}>
                        改数据
                      </Button>
                    </div>
                  );
                })}
              </>
            )}

            {/* 签名与印章：值是资源 ID，不可知，必须给选择入口。
                只列「我能用的」（自己拥有的 + 别人授权给我的）。 */}
            {signVars.length > 0 && (
              <>
                <Divider style={{ margin: '12px 0 8px' }} />
                <div className="studio-sub-title">
                  签名与印章<span>{signVars.length}</span>
                </div>
                {signVars.map((v) => {
                  const isSeal = v.name.startsWith('seal_');
                  const cur =
                    parsed.ok && parsed.value && typeof parsed.value === 'object'
                      ? (parsed.value as Record<string, unknown>)[v.name]
                      : undefined;
                  const curName = assetName(cur);
                  const extras = renderParamsOf(cur);
                  const hasTs = Boolean(
                    cur && typeof cur === 'object' && !Array.isArray(cur)
                      && ((cur as Record<string, unknown>).with_timestamp
                        || (cur as Record<string, unknown>).timestamp),
                  );
                  return (
                    <div className="studio-chart-row" key={v.name}>
                      <div className="studio-chart-info">
                        <div className="studio-chart-name" title={v.name}>
                          {isSeal ? '印章' : '签名'} · {v.name.replace(/^(seal|sig)_/, '')}
                        </div>
                        <div className="studio-chart-meta">
                          {curName ? (
                            <Tag size="small" color="green">{curName}</Tag>
                          ) : (
                            <span className="studio-chart-empty">未选择，生成时会留空</span>
                          )}
                          {/* 参数对象（现场出图）：标出来，否则用户不知道自己开过「印签署时间」 */}
                          {extras.map((x) => (
                            <Tag key={x} size="small" color="arcoblue" style={{ marginLeft: 6 }}>
                              {x}
                            </Tag>
                          ))}
                        </div>
                      </div>
                      <Space size={6}>
                        <Button
                          size="mini"
                          type="outline"
                          onClick={() => setAssetPick({ v, kind: isSeal ? 'seal' : 'signature' })}
                        >
                          {curName ? '换一个' : '选择'}
                        </Button>
                        {/* 签名专属：一键把「预置图」切成「出图时印上签署时间」。
                            本质是把值从 "sig_xxx" 换成 {"$sig":"sig_xxx","with_timestamp":true,...}，
                            JSON 里随时可见、可改。印章不印时间，所以没有这个开关。 */}
                        {!isSeal && (
                          <Button
                            size="mini"
                            type={hasTs ? 'primary' : 'text'}
                            disabled={!assetIdOf(cur)}
                            title={assetIdOf(cur) ? '开关：生成时在签名下方印上服务端时间' : '先选择签名'}
                            onClick={() => toggleTimestamp(v.name)}
                          >
                            {hasTs ? '印时间 ✓' : '印时间'}
                          </Button>
                        )}
                      </Space>
                    </div>
                  );
                })}
              </>
            )}

            <div style={{ marginTop: 12 }}>
              {(Object.keys(KIND_META) as (keyof typeof KIND_META)[]).map((k) => (
                <Tag key={k} color={KIND_META[k].color} size="small" style={{ marginBottom: 4 }}>
                  {KIND_META[k].label}
                </Tag>
              ))}
            </div>
          </div>
        </div>

        {/* 中：Monaco JSON 编辑器 */}
        <div className="df-studio-col df-studio-col-mid">
          <div className="df-studio-col-header">数据 JSON（自动存草稿）</div>
          <div className="studio-editor" style={{ flex: 1, minHeight: 0 }}>
            <Editor
              height="100%"
              defaultLanguage="json"
              value={jsonText}
              onChange={(v) => {
                setJsonText(v || '');
                setEditTick((t) => t + 1);
              }}
              onMount={handleMount}
              options={{
                minimap: { enabled: false },
                fontSize: 13,
                scrollBeyondLastLine: false,
                automaticLayout: true,
                formatOnPaste: true,
              }}
            />
          </div>
          {!parsed.ok && (
            <Alert style={{ margin: '0 12px 12px' }} type="error" content={`JSON 语法错误：${parsed.err}（编辑器中红色波浪线处）`} />
          )}
          {validateResult.length > 0 && (
            <div style={{ padding: '0 12px 12px' }}>
              <Alert type="warning" content={`校验未通过（${validateResult.length} 项）：`} style={{ marginBottom: 8 }} />
              {validateResult.map((e, i) => (
                <div key={i} style={{ fontSize: 12, marginBottom: 4, color: '#d4380d' }}>
                  {e.path ? `${e.path}：` : ''}
                  {e.message}
                </div>
              ))}
            </div>
          )}
        </div>

        {/* 右：预览 */}
        <div className="df-studio-col df-studio-col-right">
          <div className="df-studio-col-header">实时预览</div>
          <div className="df-studio-col-body">
            <PreviewPane
              templateId={templateId}
              versionId={tpl.current_version_id}
              data={parsed.ok ? parsed.value : null}
              editKey={editTick}
            />
          </div>
        </div>
      </div>

      <ChartDataDrawer
        visible={chartEdit !== null}
        chart={chartEdit}
        value={
          chartEdit && parsed.ok && parsed.value && typeof parsed.value === 'object'
            ? (parsed.value as Record<string, unknown>)[chartEdit.name]
            : undefined
        }
        onClose={() => setChartEdit(null)}
        onSave={applyChart}
      />

      <SignAssetPicker
        visible={assetPick !== null}
        kind={assetPick?.kind || 'signature'}
        variableName={assetPick?.v.name || ''}
        onClose={() => setAssetPick(null)}
        onSelect={(a) => assetPick && applyAsset(assetPick.v.name, a.id)}
      />
    </div>
  );
}

// 在编辑器文本里按 JSON path 粗定位行号（items[0].name → 找 "name" 出现的行）
function findLineForPath(text: string, path?: string): number {
  if (!path) return 1;
  const leaf = path.split('.').pop()?.replace(/\[\]$/, '').replace(/\[\d+\]/g, '') || path;
  const lines = text.split('\n');
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].includes(`"${leaf}"`)) return i + 1;
  }
  return 1;
}

// window.monaco 占位（loader.config 后全局可用）
declare global {
  interface Window {
    monaco?: typeof import('monaco-editor');
  }
}
