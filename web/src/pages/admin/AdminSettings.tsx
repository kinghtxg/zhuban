/**
 * 系统设置（admin）
 *
 * 布局规范：PageContainer + 指标块（stat-grid）+ 表单卡片
 * 信息层级：只读运行参数在上（3 列指标块，2 行正好排满），
 *          可编辑的签章审核策略在下（单卡片，水平表单，控件定宽）。
 * 渲染服务健康这类「当前状态」放页头右侧，不掺进指标块里。
 */

import { useState } from 'react';
import { Alert, Button, Card, Form, InputNumber, Message, Skeleton, Switch, Tag } from '@arco-design/web-react';
import { IconRefresh } from '@arco-design/web-react/icon';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { getAdminSettings, updateAdminSettings } from '@/api/endpoints';
import { errorMessage } from '@/utils/format';
import PageContainer from '@/components/PageContainer';
import FontManager from './FontManager';

export default function AdminSettingsPage() {
  const qc = useQueryClient();
  const { data, isLoading, isError, refetch } = useQuery({
    queryKey: ['admin-settings'],
    queryFn: () => getAdminSettings(),
    retry: false,
  });
  const health = useQuery({ queryKey: ['healthz'], queryFn: () => fetch('/api/v1/healthz').then((r) => r.json()), retry: false });

  const [sigReview, setSigReview] = useState(false);
  const [sealReview, setSealReview] = useState(false);
  const [sigWidth, setSigWidth] = useState(60);
  const [sealWidth, setSealWidth] = useState(40);

  // 拉到数据后回填本地开关
  const [hydrated, setHydrated] = useState(false);
  const d = data as typeof data & {
    review_signatures_enabled?: boolean;
    review_seals_enabled?: boolean;
    signature_default_width_mm?: number;
    seal_default_width_mm?: number;
  };
  if (d && !hydrated) {
    setSigReview(!!d.review_signatures_enabled);
    setSealReview(!!d.review_seals_enabled);
    setSigWidth(d.signature_default_width_mm ?? 60);
    setSealWidth(d.seal_default_width_mm ?? 40);
    setHydrated(true);
  }

  const save = async () => {
    try {
      await updateAdminSettings({
        review_signatures_enabled: sigReview,
        review_seals_enabled: sealReview,
        signature_default_width_mm: sigWidth,
        seal_default_width_mm: sealWidth,
      });
      Message.success('已保存并即时生效');
      qc.invalidateQueries({ queryKey: ['admin-settings'] });
    } catch (err) {
      Message.error(errorMessage(err).summary);
    }
  };

  if (isLoading) {
    return (
      <div className="df-page">
        <Skeleton animation />
      </div>
    );
  }

  if (isError || !data) {
    return (
      <div className="df-page">
        <Alert
          type="warning"
          action={<Button size="small" icon={<IconRefresh />} onClick={() => refetch()}>重试</Button>}
          content="系统设置接口尚未就绪（后端开发中）。待 GET /api/v1/admin/settings 上线后展示。"
        />
      </div>
    );
  }

  const items = [
    { label: '产物保留期', value: data.retention_days != null ? `${data.retention_days} 天` : '—' },
    { label: '签名直链有效期', value: data.signed_url_ttl_hours != null ? `${data.signed_url_ttl_hours} 小时` : '—' },
    { label: '单任务最大条数', value: data.max_records != null ? String(data.max_records) : '—' },
    { label: 'JSON 大小上限', value: data.max_json_mb != null ? `${data.max_json_mb} MB` : '—' },
    { label: '回调白名单', value: data.callback_allowlist || '—' },
    { label: 'OIDC 登录', value: data.oidc_enabled ? '已开启' : '未开启' },
  ];

  return (
    <PageContainer
      title="系统设置"
      subtitle="关键运行参数与签章审核策略"
      extra={
        <>
          <span className="cell-sub">渲染服务</span>
          {health.isLoading ? (
            <Tag size="small">检测中</Tag>
          ) : health.data ? (
            <Tag size="small" color={health.data.render === 'ok' ? 'green' : 'red'}>
              {health.data.render === 'ok' ? '正常' : `异常（${health.data.render}）`}
            </Tag>
          ) : (
            <Tag size="small" color="red">不可达</Tag>
          )}
          <Button size="small" icon={<IconRefresh />} onClick={() => { refetch(); health.refetch(); }}>
            刷新
          </Button>
        </>
      }
    >
      {/*
        这一块以前是六个裸数字直接铺在页面顶部，和下面真正能改的表单连在一起，
        于是「产物保留期」这种只读值看起来也像可以改——用户改不动就去怀疑是不是坏了。
        包一层带标题的 Card 并把来源说清楚：这些值来自部署配置，改这里没用。
      */}
      <Card
        title="运行参数"
        className="section"
        extra={<span className="cell-sub">只读 · 由部署配置决定</span>}
      >
        <div className="stat-grid is-3">
          {items.map((it) => (
            <div className="stat-block" key={it.label}>
              <div className="stat-block-label">{it.label}</div>
              <div className="stat-block-value">{it.value}</div>
            </div>
          ))}
        </div>
        <div className="cell-sub" style={{ marginTop: 12, lineHeight: 1.8 }}>
          以上数值来自服务端环境变量（<code>.env</code>），修改后需重启服务。
          本页不能编辑，是有意的：保留期与大小上限一旦可在线改写，
          一次误操作就会让磁盘被历史产物填满。
        </div>
      </Card>

      <Card title="签章审核" className="section">
        <Form
          layout="horizontal"
          labelAlign="left"
          labelCol={{ span: 6 }}
          wrapperCol={{ span: 12 }}
          style={{ maxWidth: 760 }}
        >
          <Form.Item label="开启签名审核" extra="开启后新上传的签名需审核通过才可用于生成">
            <Switch checked={sigReview} onChange={setSigReview} />
          </Form.Item>
          <Form.Item label="开启印章审核" extra="开启后新上传的印章需审核通过才可用于生成">
            <Switch checked={sealReview} onChange={setSealReview} />
          </Form.Item>
          <Form.Item label="签名插入默认宽度">
            <InputNumber value={sigWidth} onChange={(v) => setSigWidth(Number(v) || 60)} suffix="mm" style={{ width: 160 }} />
          </Form.Item>
          <Form.Item label="印章插入默认宽度">
            <InputNumber value={sealWidth} onChange={(v) => setSealWidth(Number(v) || 40)} suffix="mm" style={{ width: 160 }} />
          </Form.Item>
        </Form>

        <div className="cell-sub" style={{ marginBottom: 12 }}>
          开关切换即时生效，不影响已通过审核的历史资源。
        </div>
        <Button type="primary" onClick={save}>
          保存
        </Button>
      </Card>

      <Card
        title="邮件服务"
        className="section"
        extra={
          <Button size="small" type="outline" onClick={() => { window.location.href = '/admin/mail'; }}>
            前往配置
          </Button>
        }
      >
        <div className="cell-sub" style={{ lineHeight: 1.8 }}>
          SMTP 服务器、发件人与收件人域名白名单在「管理中心 → 邮件服务」配置。
          配置完成后，API 生成请求可携带 <code>email</code> / <code>emails</code> 参数指定收件人，
          产物渲染完成即自动投递。
        </div>
      </Card>

      <Card title="字体管理" className="section">
        <FontManager />
      </Card>
    </PageContainer>
  );
}
