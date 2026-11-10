import { useMemo, useState } from 'react';
import { Alert, Button, Card, Descriptions, Message, Tabs, Tag } from '@arco-design/web-react';
import { IconCopy } from '@arco-design/web-react/icon';
import { useNavigate, useParams } from 'react-router-dom';
import { useMutation, useQuery } from '@tanstack/react-query';
import { getMe, getTemplate, getTemplateFontAudit } from '@/api/endpoints';
import FontAuditNotice from '@/components/FontAuditNotice';
import { buildSamples } from '@/components/integrationSamples';
import { SEVERITY_META, VISIBILITY_META, lookupMeta } from '@/constants/enums';
import { errorMessage, fmtTime } from '@/utils/format';
import type { FontAudit } from '@/types/api';

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

  // 按需重算字体体检。页面默认展示**录入时的快照**（那是当时真实情况），
  // 点了「重新检查」才换成此刻的结论，并把两份都留下以便说清差异。
  const [fresh, setFresh] = useState<FontAudit | null>(null);
  const [recorded, setRecorded] = useState<FontAudit | null>(null);
  const recheck = useMutation({
    mutationFn: () => getTemplateFontAudit(id),
    onSuccess: (r) => {
      setFresh(r.font_audit);
      setRecorded(r.recorded);
      const before = r.recorded?.missing?.length ?? 0;
      const now = r.font_audit?.missing?.length ?? 0;
      if (now === 0) {
        Message.success(before > 0 ? `现在不缺字体了（录入时缺 ${before} 种）` : '字体检查通过');
      } else {
        Message.warning(`仍有 ${now} 种字体本机没有`);
      }
    },
    onError: (err) => {
      const { summary } = errorMessage(err);
      Message.error(`重新检查失败：${summary}`);
    },
  });

  if (!tpl) return null;
  const diags = tpl.current_version?.diagnostics || [];
  const vis = lookupMeta(VISIBILITY_META as Record<string, { label: string; color: string }>, tpl.visibility);
  const audit = fresh ?? tpl.current_version?.font_audit;
  const auditBad = (audit?.ok && audit.missing.length > 0) || false;

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
      {/* 字体问题只在这一版录入时存在过，却是最容易被忽略的那类：
          接口 200、正文全对、PDF 能打开，只有字形被换掉。
          所以它单独占一条横幅（有问题时才出现），而不是塞进"诊断摘要"里
          和语法警告混在一起——那两类事的处理方式完全不同。 */}
      {auditBad && (
        <FontAuditNotice
          style={{ marginBottom: 16 }}
          audit={audit}
          onManageFonts={() => navigate('/admin/settings')}
          onRecheck={() => recheck.mutate()}
          rechecking={recheck.isPending}
        />
      )}
      {fresh && recorded && auditBad === false && (recorded.missing?.length ?? 0) > 0 && (
        <Alert
          style={{ marginBottom: 16 }}
          type="success"
          content={`字体已补齐：录入这个版本时缺 ${recorded.missing.length} 种（${recorded.missing.join('、')}），现在都不缺了。`}
        />
      )}
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

      {/* 缺字体时页首已经有完整横幅（含重新检查），这里再报一遍只是重复。
          卡片只在"没有横幅"的时候出现，负责回答「字体到底行不行」并提供重算入口。 */}
      {!auditBad && (
      <Card title="字体可用性" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
          <span style={{ color: 'var(--color-text-2)' }}>
            {audit
              ? audit.ok
                  ? `模板用到的 ${audit.checked} 种字体本机都有，导出字形与模板一致`
                  : `本次没能完成检查${audit.error ? `（${audit.error}）` : ''}`
              : '这一版录入时还没有字体体检（功能上线前创建的版本）'}
          </span>
          <Button size="mini" loading={recheck.isPending} onClick={() => recheck.mutate()}>
            重新检查
          </Button>
        </div>
      </Card>
      )}

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
