import { useEffect, useMemo, useState } from 'react';
import { useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Alert, Button, Message, Modal, Popconfirm, Radio, Space, Spin, Tag } from '@arco-design/web-react';
import { IconCheck, IconCode, IconDownload, IconRefresh, IconThumbUp } from '@arco-design/web-react/icon';
import {
  getTemplate,
  listUsableSeals,
  listUsableSignatures,
  renderDoc,
  validateData,
} from '@/api/endpoints';
import FillForm, { blankForm, isBlank, isSignatureVar } from '@/components/FillForm';
import ChartDataDrawer, { type ChartPayload } from '@/components/ChartDataDrawer';
import SignAssetPicker from '@/components/SignAssetPicker';
import PreviewPane from '@/components/PreviewPane';
import EmptyState from '@/components/EmptyState';
import { assetIdOf, replaceAsset } from '@/utils/signvalue';
import type { Format, TemplateVariable, ValidateIssue } from '@/types/api';
import { errorMessage } from '@/utils/format';

/**
 * 录入页（问卷式填写）。
 *
 * 与「试运行」的区别不是皮肤，是给谁用：
 *   试运行 = Monaco JSON 编辑器，给写接口/调模板的人；
 *   录入   = 一张表单，给要把这份文档填出来的人。
 * 后者不知道、也不需要知道数据在 JSON 里长什么样。
 *
 * 表单本身是纯受控组件（components/FillForm），这个页面负责四件事：
 * 初始化数据、存草稿、校验并标红、生成下载。
 */

type Values = Record<string, unknown>;

const DRAFT_PREFIX = 'df-fill-draft-';

export default function TemplateFill() {
  const { id: templateId = '' } = useParams();

  const { data: tpl, isLoading } = useQuery({
    queryKey: ['template', templateId],
    queryFn: () => getTemplate(templateId),
    enabled: !!templateId,
  });

  const vars = useMemo<TemplateVariable[]>(() => tpl?.current_version?.variables || [], [tpl]);

  const [values, setValues] = useState<Values>({});
  const [ready, setReady] = useState(false);
  const [format, setFormat] = useState<Format>('docx');
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [issues, setIssues] = useState<ValidateIssue[]>([]);
  const [busy, setBusy] = useState<'validate' | 'render' | null>(null);
  const [jsonOpen, setJsonOpen] = useState(false);
  // 抽屉里记 apply，而不是只记变量名：同一个变量可能出现在循环的第 3 项，
  // 只按名字回写会写到顶层去（见 FillForm 的 props 注释）。
  const [chartEdit, setChartEdit] = useState<{
    v: TemplateVariable;
    value: unknown;
    apply: (next: unknown) => void;
  } | null>(null);
  const [assetPick, setAssetPick] = useState<{
    v: TemplateVariable;
    value: unknown;
    apply: (next: unknown) => void;
  } | null>(null);

  const draftKey = `${DRAFT_PREFIX}${templateId}`;

  // 初始化：优先恢复草稿，否则给一张空表。
  // 只在拿到模板后跑一次——之后用户改什么就是什么，不能被重渲染冲掉。
  useEffect(() => {
    if (!tpl || ready) return;
    const saved = localStorage.getItem(draftKey);
    if (saved) {
      try {
        const o = JSON.parse(saved) as unknown;
        if (o && typeof o === 'object' && !Array.isArray(o)) {
          // 草稿盖在空表上，而不是直接当整份数据用：模板发了新版本、多了几个变量时，
          // 老草稿里没有那些键。直接用它会让新字段拿到 undefined——字段显示成空，
          // 但循环类字段连「添加一项」的形状都没有。
          setValues({ ...blankForm(tpl.current_version?.variables || []), ...(o as Values) });
          setReady(true);
          return;
        }
      } catch {
        // 草稿坏了就用空表，不拦着人填
      }
    }
    setValues(blankForm(tpl.current_version?.variables || []));
    setReady(true);
  }, [tpl, ready, draftKey]);

  // 草稿实时存。业务人员填到一半关掉页面是常态，丢一次就再也不用了。
  useEffect(() => {
    if (!ready) return;
    try {
      localStorage.setItem(draftKey, JSON.stringify(values));
    } catch {
      // 存不下（隐私模式/超配额）就算了，不影响填写
    }
  }, [values, ready, draftKey]);

  // 签章资源 id -> 名字。值是 ID，不翻成人名没人看得懂自己选的是哪个。
  const { data: usableData } = useQuery({
    queryKey: ['usable-assets', 'both'],
    queryFn: async () => {
      const [s, e] = await Promise.all([listUsableSignatures(), listUsableSeals()]);
      const map: Record<string, { name: string; kind: 'signature' | 'seal' }> = {};
      for (const a of s.items) map[a.id] = { name: a.name, kind: 'signature' };
      for (const a of e.items) map[a.id] = { name: a.name, kind: 'seal' };
      return map;
    },
    enabled: vars.some((v) => v.name.startsWith('sig_') || v.name.startsWith('seal_')),
  });

  // 必填统计：实时显示「必填 12 · 已填 5」，但不实时标红——
  // 一进页面满屏红字会让人以为已经填错了东西。
  const requiredVars = useMemo(
    () => vars.filter((v) => v.required && !v.system_resolved && v.kind !== 'chart'),
    [vars],
  );
  const missingCount = useMemo(
    () => requiredVars.filter((v) => isBlank(v, values[v.name])).length,
    [requiredVars, values],
  );
  const doneCount = requiredVars.length - missingCount;

  const globalIssues = issues.filter((e) => !e.path);

  /** 必填为空时说什么。统一一句「这一项必填」等于没说清该干什么。 */
  const missMsg = (v: TemplateVariable): string => {
    if (v.kind === 'image') return isSignatureVar(v) ? '请选择一个签名/印章' : '请选择一张图片';
    if (v.kind === 'richtext') return '这一段不能空着';
    if (v.kind === 'qrcode') return '请填写二维码里的内容';
    if (v.kind === 'loop') return '至少填一项';
    return '这一项必填';
  };

  /**
   * 真正要发给接口的数据。
   *
   * 为什么不直接把表单的 values 交出去：图片/富文本/二维码的 schema 只接受对象，
   * 空值写成 '' 会被判「应为对象」直接 422。非必填的空字段干脆不出现——
   * 模板里那个占位符就渲染成空，这正是「没填」该有的样子。
   */
  const payloadOf = (): Values => {
    const meta = new Map(vars.map((v) => [v.name, v]));
    const out: Values = {};
    for (const [k, val] of Object.entries(values)) {
      const v = meta.get(k);
      if (!v) {
        out[k] = val; // 草稿里多出来的旧键，原样带着不影响渲染
        continue;
      }
      if (isBlank(v, val) && !v.required) continue;
      out[k] = val;
    }
    return out;
  };

  /** 把校验返回的 path 展开成表单认识的几种写法。 */
  const mapIssues = (list: ValidateIssue[]): Record<string, string> => {
    const out: Record<string, string> = {};
    for (const e of list) {
      if (!e.path) continue;
      const raw = e.path.replace(/^\$\.?/, '').replace(/^data\./, '').trim();
      const variants = new Set<string>([
        raw,
        raw.replace(/\.(\d+)\./g, '[$1].'), // 明细.0.金额 -> 明细[0].金额
        raw.replace(/\[(\d+)\]\./g, '.$1.'), // 明细[0].金额 -> 明细.0.金额
      ]);
      for (const k of variants) if (!out[k]) out[k] = e.message;
      // 循环块自己的 path 上没有错误，标红会全落在子字段上；
      // 在块底部再提示一句「这项有问题」，长列表里才找得到。
      const parent = raw.match(/^([^[.]+)[.[]/);
      if (parent && !out[parent[1]]) out[parent[1]] = '这一项里有地方没填对';
    }
    return out;
  };

  const scrollToFirstError = () => {
    requestAnimationFrame(() => {
      document.querySelector('.df-fill-err')?.scrollIntoView({ behavior: 'smooth', block: 'center' });
    });
  };

  const runValidate = async (): Promise<boolean> => {
    const miss: Record<string, string> = {};
    for (const v of requiredVars) {
      if (isBlank(v, values[v.name])) miss[v.name] = missMsg(v);
    }
    if (Object.keys(miss).length > 0) {
      setErrors(miss);
      setIssues([]);
      Message.warning(`还有 ${Object.keys(miss).length} 项必填没填，已在表单里标出`);
      scrollToFirstError();
      return false;
    }

    const payload = payloadOf();
    setBusy('validate');
    try {
      const r = await validateData({
        template_id: templateId,
        version_id: tpl?.current_version_id,
        data: payload,
      });
      setIssues(r.errors || []);
      if (!r.valid) {
        setErrors(mapIssues(r.errors || []));
        Message.warning(`数据校验未通过（${r.errors?.length || 0} 项），已标出问题位置`);
        scrollToFirstError();
        return false;
      }
      setErrors({});
      return true;
    } catch (err) {
      Message.error(errorMessage(err).summary);
      return false;
    } finally {
      setBusy(null);
    }
  };

  const generate = async () => {
    if (!(await runValidate())) return;
    setBusy('render');
    try {
      const r = await renderDoc({
        template_id: templateId,
        version_id: tpl?.current_version_id,
        data: payloadOf(),
        format,
      });
      const doc = r.documents?.[0];
      if (r.sync && doc) {
        // pdf 请求要取 pdf_download_url：download_url 指向源 docx，
        // 拿它下载会得到 Word 文件而不是用户刚选的 PDF。
        const url =
          (format === 'pdf' ? doc.pdf_download_url : doc.download_url) ||
          doc.signed_url ||
          doc.download_url;
        if (url) {
          window.open(url, '_blank');
          Message.success(`已生成 ${doc.filename}，正在打开`);
        } else {
          Message.success('已生成，请到「文档生成中心」下载');
        }
      } else {
        Message.success('任务已提交，可在「文档生成中心」查看进度与下载');
      }
    } catch (err) {
      Message.error(errorMessage(err).summary);
    } finally {
      setBusy(null);
    }
  };

  const fillSample = () => {
    const s = tpl?.current_version?.sample_json;
    if (!s) {
      Message.warning('这个模板没有示例数据');
      return;
    }
    // 用空表兜底再覆盖示例：示例里没有的变量（比如新加的字段）也得有个位置，
    // 否则那些字段会变成 undefined，填进去的循环项形状也会错。
    setValues({ ...blankForm(vars), ...(s as Values) });
    setErrors({});
    setIssues([]);
    Message.success('已填入示例数据，把占位符改成真实内容即可');
  };

  const resetAll = () => {
    setValues(blankForm(vars));
    setErrors({});
    setIssues([]);
    localStorage.removeItem(draftKey);
    Message.success('已清空，重新填一份');
  };

  if (isLoading) {
    return (
      <div className="df-page" style={{ padding: 80, display: 'flex', justifyContent: 'center' }}>
        <Spin />
      </div>
    );
  }
  if (!tpl) {
    return (
      <div className="df-page">
        <EmptyState description="模板不存在或已停用" />
      </div>
    );
  }
  if (!ready) return <div className="df-page" />;

  const countCls = missingCount === 0 ? 'df-fill-count is-ready' : 'df-fill-count';

  return (
    <div className="df-fill-page">
      {/* 左：问卷 */}
      <div className="df-studio-col df-fill-form-col">
        <div className="df-studio-col-header">
          填写内容
          <span className={countCls}>
            {missingCount === 0 ? (
              <>
                <IconCheck /> 必填 {requiredVars.length} 项都填好了
              </>
            ) : (
              <>
                必填 {requiredVars.length} 项 · 已填 {doneCount}
              </>
            )}
          </span>
        </div>

        <div className="df-studio-col-body">
          {issues.length > 0 && (
            <Alert
              type="warning"
              style={{ marginBottom: 12 }}
              content={
                <div>
                  <div style={{ fontWeight: 500, marginBottom: 4 }}>
                    有 {issues.length} 处需要调整：
                  </div>
                  {globalIssues.length === 0 && (
                    <div style={{ fontSize: 12, color: 'var(--color-text-3)' }}>
                      具体位置已在下面标红
                    </div>
                  )}
                  {globalIssues.map((e, i) => (
                    <div key={i} style={{ fontSize: 12 }}>
                      {e.message}
                    </div>
                  ))}
                </div>
              }
            />
          )}
          <FillForm
            variables={vars}
            values={values}
            onChange={setValues}
            errors={errors}
            onPickAsset={(v, value, apply) => setAssetPick({ v, value, apply })}
            onEditChart={(v, value, apply) => setChartEdit({ v, value, apply })}
            assetLabel={(_v, value) => {
              const id = assetIdOf(value);
              return id ? usableData?.[id]?.name || id : '';
            }}
          />
        </div>

        {/* 操作条固定在列底部：长表单滚到第 40 行时不用再滚回顶部找按钮 */}
        <div className="df-fill-actions">
          <Space size={4} wrap>
            <Popconfirm title="清空后已填内容不可恢复（草稿也会删掉），确定？" onConfirm={resetAll}>
              <Button size="small" type="text" icon={<IconRefresh />}>
                清空重填
              </Button>
            </Popconfirm>
            <Button size="small" type="text" icon={<IconThumbUp />} onClick={fillSample}>
              填示例数据
            </Button>
            <Button size="small" type="text" icon={<IconCode />} onClick={() => setJsonOpen(true)}>
              看数据
            </Button>
          </Space>
          <Space size={8}>
            <Radio.Group
              type="button"
              size="small"
              value={format}
              onChange={(v) => setFormat(v as Format)}
            >
              <Radio value="docx">docx</Radio>
              <Radio value="pdf">pdf</Radio>
            </Radio.Group>
            <Button
              type="primary"
              icon={<IconDownload />}
              loading={busy !== null}
              onClick={generate}
            >
              生成并下载
            </Button>
          </Space>
        </div>
      </div>

      {/* 右：实时预览 */}
      <div className="df-studio-col df-fill-preview-col">
        <div className="df-studio-col-header">
          实时预览
          <span className="df-fill-count">停止输入约 2 秒后刷新</span>
        </div>
        <div className="df-studio-col-body">
          <PreviewPane
            templateId={templateId}
            versionId={tpl.current_version_id}
            data={values}
            editKey={0}
          />
        </div>
      </div>

      <ChartDataDrawer
        visible={chartEdit !== null}
        chart={chartEdit?.v || null}
        value={chartEdit?.value}
        onClose={() => setChartEdit(null)}
        onSave={(_name, payload: ChartPayload) => {
          chartEdit?.apply(payload);
          setChartEdit(null);
          Message.success('图表数据已写入，预览会自动刷新');
        }}
      />

      <SignAssetPicker
        visible={assetPick !== null}
        kind={assetPick?.v.name.startsWith('seal_') ? 'seal' : 'signature'}
        variableName={assetPick?.v.name || ''}
        onClose={() => setAssetPick(null)}
        onSelect={(a) => {
          // 换资源时保留现场出图参数（印签署时间 / 指定字体），见 utils/signvalue
          assetPick?.apply(replaceAsset(assetPick.value, a.id));
          setAssetPick(null);
        }}
      />

      <Modal
        title="这份数据长什么样"
        visible={jsonOpen}
        footer={<Button onClick={() => setJsonOpen(false)}>关闭</Button>}
        onCancel={() => setJsonOpen(false)}
        style={{ width: 720 }}
      >
        <div style={{ marginBottom: 8, fontSize: 12, color: 'var(--color-text-3)' }}>
          表单填的内容会变成下面这段数据。想让系统/别人批量生成时，照这个格式传即可。
          {tpl.code ? <Tag size="small" style={{ marginLeft: 6 }}>模板 {tpl.code}</Tag> : null}
        </div>
        <pre className="df-fill-json">{JSON.stringify(payloadOf(), null, 2)}</pre>
      </Modal>
    </div>
  );
}
