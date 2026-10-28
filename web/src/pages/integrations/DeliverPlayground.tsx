import { useEffect, useMemo, useState } from 'react';
import { Alert, Button, Card, Descriptions, Input, Message, Select, Space, Switch, Tag } from '@arco-design/web-react';
import { IconCopy } from '@arco-design/web-react/icon';
// 编辑器初始化是副作用模块，必须在这里引入（而不是入口）：
// 这样 Monaco 只跟着这个页面按需下载。
import '@/monaco-setup';
import Editor from '@monaco-editor/react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { generate, getTemplate, listTemplates } from '@/api/endpoints';
import type { GenerateEmailSpec, GenerateResp } from '@/types/api';
import { errorMessage } from '@/utils/format';

// 投递即生成·在线试调（PRD §4.9）：选模板 → 填 JSON → 发送 → 看产物。
// 用当前会话 cookie 调 POST /generate，不消耗 API Key。
export default function DeliverPlayground() {
  const [tplId, setTplId] = useState('');
  const [jsonText, setJsonText] = useState('');
  const [result, setResult] = useState<GenerateResp | null>(null);
  // 邮件选项：对应 POST /generate 的 email 字段。SMTP 由服务端配置，
  // 这里只能填收件人/主题/正文——这条边界和 API 完全一致。
  const [withEmail, setWithEmail] = useState(false);
  const [mailTo, setMailTo] = useState('');
  const [mailSubject, setMailSubject] = useState('');
  const [mailBody, setMailBody] = useState('');
  const [mailAttach, setMailAttach] = useState<GenerateEmailSpec['attach']>('auto');

  const { data: tpls } = useQuery({
    queryKey: ['templates', { all: true }],
    queryFn: () => listTemplates({ page: 1, page_size: 200 }),
  });

  // 选中模板后拉详情，用 sample_json 预填
  const { data: detail } = useQuery({
    queryKey: ['template', tplId],
    queryFn: () => getTemplate(tplId),
    enabled: !!tplId,
  });

  useEffect(() => {
    const s = detail?.current_version?.sample_json;
    if (s) setJsonText(JSON.stringify(s, null, 2));
  }, [detail]);

  const parsed = useMemo(() => {
    if (!jsonText.trim()) return { ok: true, value: null as unknown };
    try {
      return { ok: true, value: JSON.parse(jsonText) as unknown };
    } catch (e) {
      return { ok: false, value: null, err: e instanceof Error ? e.message : 'JSON 语法错误' };
    }
  }, [jsonText]);

  const mailToList = mailTo
    .split(/[,;\s]+/)
    .map((s) => s.trim())
    .filter(Boolean);

  const sendMut = useMutation({
    mutationFn: () =>
      generate({
        template_code: detail?.code,
        data: parsed.value,
        format: 'docx',
        external_ref: `ui-trial-${Date.now()}`,
        ...(withEmail && mailToList.length > 0
          ? {
              email: {
                to: mailToList,
                subject: mailSubject || undefined,
                body: mailBody || undefined,
                attach: mailAttach,
              },
            }
          : {}),
      }),
    onSuccess: (r) => {
      setResult(r);
      Message.success('已提交');
    },
    onError: (err) => Message.error(errorMessage(err).summary),
  });

  const selected = tpls?.items.find((t) => t.id === tplId);

  // 按钮为什么不可点，直接说出来。四类原因优先级从"最该先解决"往下排。
  const blockReason = !tplId
    ? '请先选择模板'
    : !parsed.ok
      ? 'JSON 有语法错误'
      : parsed.value === null
        ? '请填写数据（不能为空）'
        : withEmail && mailToList.length === 0
          ? '已开启自动发邮件，请至少填一个收件人'
          : '';

  return (
    <div className="df-page">
      <div className="df-page-header">
        <div>
          <h2 className="df-page-title">投递即生成</h2>
          <div className="df-page-sub">选模板 → 填 JSON → 一键试跑 → 看产物。本页用当前会话鉴权，不消耗 API Key</div>
        </div>
      </div>

      <Card style={{ marginBottom: 16 }}>
        <Space>
          <span>选择模板：</span>
          <Select
            showSearch
            placeholder="选择模板（带 code）"
            style={{ width: 360 }}
            value={tplId || undefined}
            onChange={(v) => setTplId(v)}
          >
            {(tpls?.items || []).map((t) => (
              <Select.Option key={t.id} value={t.id}>
                {t.name}（{t.code}）
              </Select.Option>
            ))}
          </Select>
          {selected && <Tag color="gray">code: {selected.code}</Tag>}
        </Space>
      </Card>

      <Card title="示例 JSON（已按模板 sample_json 预填，可改）" style={{ marginBottom: 16 }}>
        {/*
          没选模板时不要挂一个 320px 高的空编辑器：一个纯白大框 + 一行行号，
          看起来像"页面没加载出来"。这里换成等高的说明块，告诉用户下一步做什么。
        */}
        {tplId ? (
          <div style={{ height: 320, border: '1px solid var(--color-border-2)', borderRadius: 6, overflow: 'hidden' }}>
            <Editor
              height="100%"
              defaultLanguage="json"
              value={jsonText}
              onChange={(v) => setJsonText(v || '')}
              options={{ minimap: { enabled: false }, fontSize: 13, automaticLayout: true }}
            />
          </div>
        ) : (
          <div className="playground-empty">
            <div className="playground-empty-title">先在上方选择一个模板</div>
            <div className="playground-empty-sub">
              选中后这里会自动填入它的示例数据，改成你要生成的内容即可。
              <br />
              模板必须有 <code>code</code>，接口用的是 code 而不是内部 ID。
            </div>
          </div>
        )}
        {!parsed.ok && <Alert type="error" style={{ marginTop: 8 }} content={`JSON 语法错误：${parsed.err}`} />}

        <div style={{ marginTop: 16, marginBottom: 12 }}>
          <Space>
            <Switch checked={withEmail} onChange={setWithEmail} />
            <span>生成后自动发邮件</span>
          </Space>
        </div>

        {withEmail && (
          <Card title="邮件选项（对应 API 的 email 字段）" style={{ marginBottom: 16 }}>
            {/*
              SMTP 那句话以前挂在开关下面，开关关着也一直显示——用户还没打算发邮件，
              就先读到一段「服务器/白名单由管理员配置」。信息本身没错，但出现得太早。
              挪进展开区：只有真的打开了邮件，才需要知道它由谁配。
            */}
            <div className="cell-sub" style={{ marginBottom: 12 }}>
              SMTP 服务器、发件人与收件人域名白名单由管理员在「邮件服务」配置，本页只能指定收件人与正文。
            </div>
            <Space direction="vertical" style={{ width: '100%' }} size="medium">
              <Input
                placeholder="收件人，多个用逗号分隔；支持 {{ 变量 }}，如 {{ contact.email }}"
                value={mailTo}
                onChange={setMailTo}
              />
              <Input
                placeholder="主题，可用 {{ 变量 }}（留空则用模板名）"
                value={mailSubject}
                onChange={setMailSubject}
              />
              <Input.TextArea
                placeholder="正文，可用 {{ 变量 }}；留空则只带附件与下载链接"
                value={mailBody}
                onChange={setMailBody}
                autoSize={{ minRows: 2, maxRows: 5 }}
              />
              <Space>
                <span className="cell-sub">附件：</span>
                <Select value={mailAttach} onChange={setMailAttach} style={{ width: 240 }}>
                  <Select.Option value="auto">跟随任务格式（一份就带那份，多份带 zip）</Select.Option>
                  <Select.Option value="docx">仅 docx</Select.Option>
                  <Select.Option value="pdf">仅 pdf</Select.Option>
                  <Select.Option value="both">docx + pdf</Select.Option>
                  <Select.Option value="zip">整包 zip</Select.Option>
                  <Select.Option value="none">不带附件</Select.Option>
                </Select>
              </Space>
            </Space>
          </Card>
        )}

        {/*
          禁用按钮要说清为什么禁用。原来只有一个淡蓝色的不可点按钮，
          用户看不出是"少选了模板"还是"JSON 写错了"。
        */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <Button
            type="primary"
            loading={sendMut.isPending}
            disabled={!!blockReason}
            onClick={() => sendMut.mutate()}
          >
            发送（POST /generate）
          </Button>
          {blockReason && <span className="cell-sub">{blockReason}</span>}
        </div>
      </Card>

      {result && (
        <Card title="结果">
          <Descriptions
            column={1}
            size="medium"
            data={[
              { label: 'job_id', value: result.job_id },
              { label: '状态', value: <Tag color="arcoblue">{result.status}</Tag> },
              { label: '同步', value: result.sync ? '是' : '否' },
              ...(result.email_status
                ? [
                    { label: '邮件投递', value: <Tag color={result.email_status === 'sent' ? 'green' : result.email_status === 'failed' ? 'red' : 'orange'}>{result.email_status}</Tag> },
                    ...(result.email_error ? [{ label: '邮件错误', value: result.email_error }] : []),
                  ]
                : []),
            ]}
          />
          {result.documents && result.documents.length > 0 && (
            <div style={{ marginTop: 12 }}>
              {result.documents.map((d) => (
                <div key={d.document_id} style={{ marginBottom: 8 }}>
                  <Space>
                    <span>{d.filename}</span>
                    {d.signed_url && (
                      <Button
                        size="mini"
                        icon={<IconCopy />}
                        onClick={async () => {
                          await navigator.clipboard.writeText(abs(d.signed_url!));
                          Message.success('签名直链已复制（24 小时有效）');
                        }}
                      >
                        复制签名直链
                      </Button>
                    )}
                    {d.download_url && (
                      <Button size="mini" onClick={() => window.open(abs(d.download_url!), '_blank')}>下载</Button>
                    )}
                  </Space>
                </div>
              ))}
            </div>
          )}
          {!result.sync && result.status_url && (
            <Alert type="info" style={{ marginTop: 12 }} content={`异步任务，请在「投递任务」查看进度与下载。`} />
          )}
        </Card>
      )}
    </div>
  );
}

function abs(p: string) {
  if (!p) return p;
  return p.startsWith('http') ? p : `${window.location.origin}${p.startsWith('/') ? '' : '/api/v1/'}${p}`;
}
