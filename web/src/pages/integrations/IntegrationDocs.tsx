import { useState } from 'react';
import { Alert, Button, Card, Collapse, Empty, Message, Space, Table, Tabs, Tag } from '@arco-design/web-react';
import { IconCopy } from '@arco-design/web-react/icon';
import { useQuery } from '@tanstack/react-query';
import { getMe, listTemplates } from '@/api/endpoints';
import { buildSamples } from '@/components/integrationSamples';

const TabPane = Tabs.TabPane;
const CollapseItem = Collapse.Item;

function copyText(text: string) {
  navigator.clipboard.writeText(text).then(
    () => Message.success('已复制'),
    () => Message.error('复制失败'),
  );
}

const REQ_FIELDS = [
  { field: 'template_code', required: '是', desc: '模板编号，如 audit-report' },
  { field: 'version', required: '否', desc: '指定版本号，缺省用当前线上版本' },
  { field: 'data', required: '二选一', desc: '单份数据对象；与 records 二选一' },
  { field: 'records', required: '二选一', desc: '批量数据数组，多条' },
  { field: 'format', required: '否', desc: 'docx | pdf | both，默认 docx' },
  { field: 'filename', required: '否', desc: '文件名规则，仅替换顶层变量，如 {{项目编号}}_审计报告' },
  { field: 'callback_url', required: '否', desc: '任务终结后回调该地址（需命中白名单）' },
  { field: 'external_ref', required: '否', desc: '幂等键，同一 Key 重复提交直接返回原任务' },
  { field: 'sync', required: '否', desc: 'true 时同步等待；单份且 JSON ≤1MB 默认同步' },
];

const ERROR_CODES = [
  { code: 'template_disabled', meaning: '模板已停用', fix: '在模板库重新启用' },
  { code: 'data_invalid', meaning: '数据校验失败（缺字段/类型不符）', fix: '按 details[].path 检查 JSON' },
  { code: 'template_invalid', meaning: 'Word 占位符被样式拆坏', fix: '回 Word 用纯文本重输占位符' },
  { code: 'conversion_failed', meaning: 'LibreOffice 转 PDF 失败', fix: '重试；仍失败联系管理员' },
  { code: 'payload_too_large', meaning: 'JSON 超限或记录数超限', fix: '拆分任务或减小数据' },
];

// 回调与接入文档（PRD §4.10）：接入文档 + 回调日志。
// 回调日志：后端暂无回调记录查询接口，先放说明占位，不编造接口。
export default function IntegrationDocs() {
  const [tab, setTab] = useState('docs');
  const { data: me } = useQuery({ queryKey: ['me'], queryFn: getMe });
  const { data: tpls } = useQuery({
    queryKey: ['templates', { all: true }],
    queryFn: () => listTemplates({ page: 1, page_size: 200 }),
  });

  const base = (me?.base_url || 'http://你的服务器地址').replace(/\/$/, '');

  return (
    <div className="df-page">
      <div className="df-page-header">
        <div>
          <h2 className="df-page-title">回调与接入文档</h2>
          <div className="df-page-sub">按模板查看调用示例、请求体字段与错误码；回调日志见第二个页签</div>
        </div>
      </div>

      <Tabs activeTab={tab} onChange={setTab}>
        <TabPane key="docs" title="接入文档">
          <Card title="按模板查看调用示例（POST /api/v1/generate）" style={{ marginBottom: 16 }}>
            <Collapse>
              {(tpls?.items || []).map((t) => (
                <CollapseItem key={t.id} header={`${t.name}（${t.code}）`} name={t.id}>
                  <SampleBlock base={base} code={t.code} />
                </CollapseItem>
              ))}
            </Collapse>
          </Card>

          <Card title="请求体字段" style={{ marginBottom: 16 }}>
            <Table
              rowKey="field"
              size="small"
              pagination={false}
              data={REQ_FIELDS}
              columns={[
                { title: '字段', dataIndex: 'field', width: 180, render: (v: string) => <code>{v}</code> },
                { title: '必填', dataIndex: 'required', width: 90, render: (v: string) => <Tag size="small">{v}</Tag> },
                { title: '说明', dataIndex: 'desc' },
              ]}
            />
          </Card>

          <Card title="错误码" style={{ marginBottom: 16 }}>
            <Table
              rowKey="code"
              size="small"
              pagination={false}
              data={ERROR_CODES}
              columns={[
                { title: '错误码', dataIndex: 'code', width: 200, render: (v: string) => <Tag color="red" size="small">{v}</Tag> },
                { title: '含义', dataIndex: 'meaning' },
                { title: '怎么改', dataIndex: 'fix' },
              ]}
            />
          </Card>

          <Card title="签名直链说明">
            <Alert
              type="info"
              content={
                <div style={{ lineHeight: 1.8 }}>
                  任务终结后，响应体或回调 downloads 中的 <code>signed_url</code> 为 HMAC 签名下载直链，默认 24 小时有效，
                  无需再带 API Key 即可直接下载。单份产物走 <code>{'/api/v1/s/{token}'}</code>，批量任务为 zip 包。
                  直链有效期由服务端 <code>SIGNED_URL_TTL_HOURS</code> 控制，过期后自动失效。
                </div>
              }
            />
          </Card>
        </TabPane>

        <TabPane key="callback" title="回调日志">
          <Card>
            <Empty
              description="回调记录查询接口尚未由后端提供"
            />
            <Alert
              type="warning"
              style={{ marginTop: 8 }}
              content="后端目前未暴露回调日志查询/重放接口。任务级回调状态可在「文档中心 / 投递任务」列表的「回调状态」列与任务详情中查看。待后端补齐 GET /callback-logs 后，本页将展示 时间 / job_id / callback_url / 结果 / 次数 / 重放。"
            />
          </Card>
        </TabPane>
      </Tabs>
    </div>
  );
}

function SampleBlock({ base, code }: { base: string; code: string }) {
  const samples = buildSamples(base, code, '当前版本', '{}');
  const [lang, setLang] = useState<'curl' | 'python' | 'go'>('curl');
  return (
    <div>
      <Space style={{ marginBottom: 8 }}>
        {(['curl', 'python', 'go'] as const).map((l) => (
          <Button key={l} size="mini" type={lang === l ? 'primary' : 'secondary'} onClick={() => setLang(l)}>
            {l === 'curl' ? 'cURL' : l === 'python' ? 'Python' : 'Go'}
          </Button>
        ))}
      </Space>
      <div style={{ position: 'relative' }}>
        <div className="df-codeblock" style={{ maxHeight: 360, overflow: 'auto' }}>{samples[lang]}</div>
        <Button
          size="mini"
          type="text"
          icon={<IconCopy />}
          style={{ position: 'absolute', top: 8, right: 8, color: '#fff', background: 'rgba(255,255,255,0.12)' }}
          onClick={() => copyText(samples[lang])}
        >
          复制
        </Button>
      </div>
    </div>
  );
}
