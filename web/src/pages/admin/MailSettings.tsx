/**
 * 邮件服务（admin）
 *
 * 这一页是整个邮件功能的配置入口。有一句话必须写在最前面：
 *
 *   **SMTP 凭据只在这里由管理员配置。API 侧能指定的只有收件人、主题、正文和附件模式。**
 *
 * 这不是刻意限制能力。让调用方自带 SMTP，等于把本系统变成一台开放中继——
 * 别人可以用我们的服务器往任意地址发信，发件域名的声誉会被花光，
 * 而 Abuse 投诉会落在域名所有者头上。所以收件人还要过一层域名白名单，
 * 且白名单为空时一律拒绝（fail-closed），不能默认放行。
 *
 * 布局规范：PageContainer + 卡片。配置表单在上（水平表单，控件定宽），
 * 测试发送与投递记录在下。
 */

import { useEffect, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Form,
  Input,
  InputNumber,
  Message,
  Select,
  Skeleton,
  Space,
  Tag,
} from '@arco-design/web-react';
import { IconRefresh } from '@arco-design/web-react/icon';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { getMailSettings, sendMailTest, updateMailSettings } from '@/api/endpoints';
import type { MailSettings } from '@/types/api';
import { errorMessage } from '@/utils/format';
import PageContainer from '@/components/PageContainer';

const TLS_OPTIONS = [
  { value: 'starttls', label: 'STARTTLS（587，先明文再升格）' },
  { value: 'tls', label: 'SSL/TLS（465，一上来就加密）' },
  { value: 'none', label: '不加密（仅内网中继）' },
];

export default function MailSettingsPage() {
  const qc = useQueryClient();
  const { data, isLoading, isError, refetch } = useQuery({
    queryKey: ['mail-settings'],
    queryFn: getMailSettings,
    retry: false,
  });

  const [form, setForm] = useState<Partial<MailSettings> & { smtp_pass?: string }>({});
  const [hydrated, setHydrated] = useState(false);
  const [testTo, setTestTo] = useState('');

  // 拉到数据后回填。密码字段永远留空——只显示「已设置」，改就整体覆盖。
  useEffect(() => {
    if (data && !hydrated) {
      setForm({
        smtp_host: data.smtp_host,
        smtp_port: data.smtp_port,
        smtp_user: data.smtp_user,
        smtp_from: data.smtp_from,
        smtp_tls: data.smtp_tls,
        mail_allowed_domains: data.mail_allowed_domains,
      });
      setHydrated(true);
    }
  }, [data, hydrated]);

  const saveMut = useMutation({
    mutationFn: (patch: Record<string, unknown>) => updateMailSettings(patch),
    onSuccess: () => {
      Message.success('已保存，立即生效（无需重启）');
      qc.invalidateQueries({ queryKey: ['mail-settings'] });
      setForm((f) => ({ ...f, smtp_pass: '' }));
    },
    onError: (e) => Message.error(errorMessage(e).summary),
  });

  const testMut = useMutation({
    mutationFn: (to: string) => sendMailTest({ to }),
    onSuccess: (_, to) => Message.success(`测试邮件已发往 ${to}，请查收`),
    onError: (e) => Message.error(errorMessage(e).summary),
  });

  const doSave = () => {
    const patch: Record<string, unknown> = {
      smtp_host: form.smtp_host ?? '',
      smtp_port: form.smtp_port ?? 587,
      smtp_user: form.smtp_user ?? '',
      smtp_from: form.smtp_from ?? '',
      smtp_tls: form.smtp_tls ?? 'starttls',
      mail_allowed_domains: form.mail_allowed_domains ?? '',
    };
    // 密码留空 = 不改动。这一点必须显式说明，否则管理员想「只改发件人」
    // 却把密码清成空串，线上发信会立刻全挂。
    if (form.smtp_pass) patch.smtp_pass = form.smtp_pass;
    saveMut.mutate(patch);
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
          content="邮件服务配置接口不可用，请确认后端已升级到 V3.2。"
        />
      </div>
    );
  }

  const limits = (data as unknown as { limits?: { max_recipients: number; max_attach_mb: number; max_body_chars: number } }).limits;

  return (
    <PageContainer
      title="邮件服务"
      subtitle="SMTP 只在服务端配置；API 侧只能指定收件人、主题、正文与附件"
      extra={
        <>
          <Tag size="small" color={data.configured ? 'green' : 'red'}>
            {data.configured ? '已配置' : '未配置'}
          </Tag>
          <Button size="small" icon={<IconRefresh />} onClick={() => refetch()}>
            刷新
          </Button>
        </>
      }
    >
      {!data.configured && (
        <Alert
          style={{ marginBottom: 16 }}
          type="warning"
          content="尚未配置 SMTP 主机。在此之前，带 email 参数的生成请求会返回 422 mail_not_configured——宁可在入口拦下，也不要让任务跑完才发现发不出去。"
        />
      )}

      <Card title="SMTP 服务器" className="section">
        <Form layout="horizontal" labelAlign="left" labelCol={{ span: 6 }} wrapperCol={{ span: 14 }} style={{ maxWidth: 820 }}>
          <Form.Item label="SMTP 主机" extra="如 smtp.qiye.163.com、smtp.exmail.qq.com">
            <Input
              value={form.smtp_host ?? ''}
              placeholder="smtp.example.com"
              onChange={(v) => setForm((f) => ({ ...f, smtp_host: v }))}
            />
          </Form.Item>
          <Form.Item label="端口" extra="常用：465（SSL）、587（STARTTLS）、25（内网中继）">
            <InputNumber
              value={form.smtp_port ?? 587}
              min={1}
              max={65535}
              onChange={(v) => setForm((f) => ({ ...f, smtp_port: Number(v) || 587 }))}
              style={{ width: 140 }}
            />
          </Form.Item>
          <Form.Item label="加密方式" extra="465 选 SSL/TLS，587 选 STARTTLS">
            <Select
              value={form.smtp_tls ?? 'starttls'}
              options={TLS_OPTIONS}
              onChange={(v) => setForm((f) => ({ ...f, smtp_tls: v as MailSettings['smtp_tls'] }))}
              style={{ width: 320 }}
            />
          </Form.Item>
          <Form.Item label="登录账号" extra="多数邮件服务要求与发件人一致">
            <Input
              value={form.smtp_user ?? ''}
              placeholder="noreply@example.com"
              onChange={(v) => setForm((f) => ({ ...f, smtp_user: v }))}
            />
          </Form.Item>
          <Form.Item
            label="登录密码"
            extra={
              data.smtp_password_set
                ? '已设置。留空表示不修改；填新值则覆盖。'
                : '当前未设置（或来自环境变量）。填了就会覆盖环境变量的值。'
            }
          >
            <Input.Password
              value={form.smtp_pass ?? ''}
              placeholder={data.smtp_password_set ? '留空则不修改' : '请输入授权码/密码'}
              onChange={(v) => setForm((f) => ({ ...f, smtp_pass: v }))}
            />
          </Form.Item>
          <Form.Item label="发件人" extra={`实际生效：${(data as unknown as { effective_from?: string }).effective_from || '—'}（留空时取登录账号）`}>
            <Input
              value={form.smtp_from ?? ''}
              placeholder="铸版 <noreply@example.com>"
              onChange={(v) => setForm((f) => ({ ...f, smtp_from: v }))}
            />
          </Form.Item>
        </Form>
        <Button type="primary" loading={saveMut.isPending} onClick={doSave}>
          保存
        </Button>
      </Card>

      <Card title="收件人域名白名单" className="section">
        <div className="cell-sub" style={{ marginBottom: 12, lineHeight: 1.8 }}>
          API 投递时，收件人域名必须命中此名单；<strong>名单为空则一律拒绝</strong>。
          支持逗号分隔的精确域名（example.com）与后缀匹配（.corp.cn 匹配自身及所有子域）。
          管理端的「测试邮件」不受此限制——运维验链路时不该被自己还没填完的名单挡住。
        </div>
        <Form layout="horizontal" labelAlign="left" labelCol={{ span: 6 }} wrapperCol={{ span: 14 }} style={{ maxWidth: 820 }}>
          <Form.Item label="允许域名">
            <Input.TextArea
              value={form.mail_allowed_domains ?? ''}
              placeholder={'example.com, .corp.cn'}
              autoSize={{ minRows: 2, maxRows: 4 }}
              onChange={(v) => setForm((f) => ({ ...f, mail_allowed_domains: v }))}
            />
          </Form.Item>
        </Form>
        <Button type="primary" loading={saveMut.isPending} onClick={doSave}>
          保存
        </Button>
        {limits ? (
          <div className="cell-sub" style={{ marginTop: 12 }}>
            单次上限：收件人 {limits.max_recipients} 个 · 附件 {limits.max_attach_mb} MB · 正文 {limits.max_body_chars} 字
          </div>
        ) : null}
      </Card>

      <Card title="连通性测试" className="section">
        <div className="cell-sub" style={{ marginBottom: 12 }}>
          发一封测试邮件验证链路是否真的通。测试邮件不走白名单，可以填任意地址。
        </div>
        <Space>
          <Input
            value={testTo}
            placeholder="收件邮箱"
            style={{ width: 280 }}
            onChange={setTestTo}
            onPressEnter={() => testTo.includes('@') && testMut.mutate(testTo)}
          />
          <Button
            type="outline"
            loading={testMut.isPending}
            disabled={!testTo.includes('@')}
            onClick={() => testMut.mutate(testTo)}
          >
            发送测试邮件
          </Button>
        </Space>
      </Card>

      <Card
        title="投递记录"
        className="section"
        extra={
          <Button size="small" type="outline" onClick={() => { window.location.href = '/admin/audit-logs'; }}>
            去审计日志查看
          </Button>
        }
      >
        <div className="cell-sub" style={{ lineHeight: 1.8 }}>
          每次投递（成功或失败）都写进<strong>哈希链审计日志</strong>（动作 <code>mail.deliver</code>，
          对象 <code>email</code>），与模板、签章等操作共用同一条防篡改链——
          可校验完整性、可导出 CSV。
          <br />
          在「审计日志」页筛选 <code>object_type = email</code> 即可只看邮件投递；
          展开任意一行能看到收件人、主题、附件、尝试次数与失败原因。
        </div>
      </Card>
    </PageContainer>
  );
}
