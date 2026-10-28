import { useMemo, useState } from 'react';
import { Alert, Button, Card, Descriptions, Message, Tabs, Tag } from '@arco-design/web-react';
import { IconCopy } from '@arco-design/web-react/icon';
import { useNavigate, useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { getMe, getTemplate } from '@/api/endpoints';
import { buildSamples } from '@/components/integrationSamples';
import { SEVERITY_META, VISIBILITY_META, lookupMeta } from '@/constants/enums';
import { fmtTime } from '@/utils/format';

const TabPane = Tabs.TabPane;

// 模板概览：基本信息 + 接入示例（curl/Python/Go）+ 诊断摘要
export default function TemplateOverview() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const [tab, setTab] = useState('curl');
  const { data: me } = useQuery({ queryKey: ['me'], queryFn: getMe });
  const { data: tpl } = useQuery({
    queryKey: ['template', id],
    queryFn: () => getTemplate(id),
    enabled: !!id,
  });

  const samples = useMemo(() => {
    if (!tpl) return null;
    const base = (me?.base_url || 'http://你的服务器地址').replace(/\/$/, '');
    const sampleJson = JSON.stringify(tpl.current_version?.sample_json || { key: 'value' }, null, 2);
    return buildSamples(base, tpl.code, tpl.current_version?.version || '1.0.0', sampleJson);
  }, [tpl, me]);

  if (!tpl) return null;
  const diags = tpl.current_version?.diagnostics || [];
  const vis = lookupMeta(VISIBILITY_META as Record<string, { label: string; color: string }>, tpl.visibility);

  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      Message.success('已复制到剪贴板');
    } catch {
      Message.error('复制失败，请手动选择复制');
    }
  };

  return (
    <div style={{ padding: '0 24px 24px' }}>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16 }}>
        <Card title="基本信息" style={{ marginBottom: 16 }}>
          <Descriptions
            column={1}
            size="medium"
            data={[
              { label: '模板编号 code', value: tpl.code },
              { label: '名称', value: tpl.name },
              { label: '分类', value: tpl.category || '—' },
              { label: '描述', value: tpl.description || '—' },
              { label: '可见性', value: <Tag color={vis.color}>{vis.label}</Tag> },
              { label: '当前版本', value: `v${tpl.current_version?.version || '—'}` },
              { label: '文件大小', value: tpl.current_version?.file_size ? `${(tpl.current_version.file_size / 1024).toFixed(1)} KB` : '—' },
              { label: '更新时间', value: fmtTime(tpl.updated_at) },
            ]}
          />
        </Card>

        <Card title="诊断摘要（最近一次扫描）" style={{ marginBottom: 16 }}>
          {diags.length === 0 ? (
            <Alert type="success" showIcon content="未发现语法诊断问题，模板可正常渲染。" />
          ) : (
            diags.map((d, i) => {
              const m = lookupMeta(SEVERITY_META, d.severity);
              return (
                <Alert
                  key={i}
                  style={{ marginBottom: 8 }}
                  type={d.severity === 'error' ? 'error' : d.severity === 'warning' ? 'warning' : 'info'}
                  content={
                    <span>
                      <Tag color={m.color} size="small">{m.label}</Tag> {d.message}
                      {d.location && <span style={{ color: 'var(--color-text-3)' }}>（位置：{d.location}）</span>}
                    </span>
                  }
                />
              );
            })
          )}
          <Button type="text" style={{ marginTop: 8 }} onClick={() => navigate(`/templates/${id}/playground`)}>
            去试运行 →
          </Button>
        </Card>
      </div>

      <Card title="接入示例（POST /api/v1/generate）">
        <Tabs activeTab={tab} onChange={setTab} type="card">
          {(['curl', 'python', 'go'] as const).map((lang) => (
            <TabPane key={lang} title={lang === 'curl' ? 'cURL' : lang === 'python' ? 'Python' : 'Go'}>
              <div style={{ position: 'relative' }}>
                <div className="df-codeblock" style={{ maxHeight: 420, overflow: 'auto' }}>
                  {samples?.[lang]}
                </div>
                <Button
                  size="mini"
                  type="text"
                  icon={<IconCopy />}
                  style={{ position: 'absolute', top: 8, right: 8, color: '#fff', background: 'rgba(255,255,255,0.12)' }}
                  onClick={() => samples && copy(samples[lang])}
                >
                  复制
                </Button>
              </div>
              <div style={{ marginTop: 10, color: 'var(--color-text-3)', fontSize: 12 }}>
                密钥请在「集成中心 / API Key」创建后替换 <code>df_在APIKey页面创建的Key</code>；返回 JSON 中的{' '}
                <code>signed_url</code> 为 24 小时有效下载直链。
              </div>
            </TabPane>
          ))}
        </Tabs>
      </Card>
    </div>
  );
}
