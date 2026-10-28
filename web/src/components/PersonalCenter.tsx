import { useState } from 'react';
import {
  Alert,
  Button,
  Descriptions,
  Drawer,
  Form,
  Input,
  Message,
  Modal,
  Space,
  Tag,
} from '@arco-design/web-react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { changePassword, unbindOidc } from '@/api/endpoints';
import type { MeResponse } from '@/types/api';
import { errorMessage } from '@/utils/format';
import { validatePassword } from '@/utils/validators';
import { fmtTime } from '@/utils/format';

interface Props {
  visible: boolean;
  onClose: () => void;
  me: MeResponse;
}

export default function PersonalCenter({ visible, onClose, me }: Props) {
  const qc = useQueryClient();
  const u = me.user;
  const [oldPw, setOldPw] = useState('');
  const [newPw, setNewPw] = useState('');
  const [confirmPw, setConfirmPw] = useState('');

  const pwMutation = useMutation({
    mutationFn: () => changePassword({ old_password: u.has_password ? oldPw : undefined, new_password: newPw }),
    onSuccess: () => {
      Message.success(u.has_password ? '密码已修改' : '登录密码已设置');
      setOldPw('');
      setNewPw('');
      setConfirmPw('');
      qc.invalidateQueries({ queryKey: ['me'] });
    },
    onError: (err) => Message.error(errorMessage(err).summary),
  });

  const unbindMutation = useMutation({
    mutationFn: unbindOidc,
    onSuccess: () => {
      Message.success('已解绑 APSSO');
      qc.invalidateQueries({ queryKey: ['me'] });
    },
    onError: (err) => Message.error(errorMessage(err).summary),
  });

  const submitPw = () => {
    if (u.has_password && !oldPw) return Message.warning('请输入当前密码');
    const perr = validatePassword(newPw);
    if (perr) return Message.warning(perr);
    if (newPw !== confirmPw) return Message.warning('两次输入的新密码不一致');
    pwMutation.mutate();
  };

  return (
    <Drawer title="个人中心" visible={visible} onCancel={onClose} width={420} unmountOnExit>
      <Descriptions
        column={1}
        title="基本信息"
        data={[
          { label: '用户名', value: u.username },
          { label: '显示名', value: u.display_name || '—' },
          { label: '邮箱', value: u.email || '—' },
          {
            label: '角色',
            value: <Tag color={u.role === 'admin' ? 'red' : 'arcoblue'}>{u.role === 'admin' ? '管理员' : '成员'}</Tag>,
          },
          {
            label: '来源',
            value: <Tag>{u.source === 'local' ? '本地账号' : 'APSSO'}</Tag>,
          },
          { label: '最后登录', value: fmtTime(u.last_login_at) },
        ]}
        style={{ marginBottom: 24 }}
      />

      <div style={{ fontWeight: 600, marginBottom: 12 }}>{u.has_password ? '修改登录密码' : '设置登录密码'}</div>
      <Form layout="vertical">
        {u.has_password && (
          <Form.Item label="当前密码">
            <Input.Password value={oldPw} onChange={setOldPw} placeholder="当前登录密码" />
          </Form.Item>
        )}
        <Form.Item label="新密码" required>
          <Input.Password value={newPw} onChange={setNewPw} placeholder="至少 8 位，须同时含字母和数字" />
        </Form.Item>
        <Form.Item label="确认新密码" required>
          <Input.Password value={confirmPw} onChange={setConfirmPw} placeholder="再次输入新密码" />
        </Form.Item>
        <Button type="primary" loading={pwMutation.isPending} onClick={submitPw}>
          {u.has_password ? '保存修改' : '设置密码'}
        </Button>
      </Form>

      <div style={{ fontWeight: 600, margin: '24px 0 12px' }}>APSSO 统一登录</div>
      {u.oidc_bound ? (
        <>
          <Alert type="success" style={{ marginBottom: 12 }} content="当前账号已绑定 APSSO，可使用 APSSO 登录。" />
          <Button
            status="danger"
            loading={unbindMutation.isPending}
            disabled={!u.has_password}
            onClick={() =>
              Modal.confirm({
                title: '确认解绑 APSSO？',
                content: '解绑后将只能用本地账号密码登录。',
                onOk: () => unbindMutation.mutate(),
              })
            }
          >
            解绑 APSSO
          </Button>
          {!u.has_password && (
            <div style={{ color: 'var(--color-text-3)', fontSize: 12, marginTop: 8 }}>未设置本地密码前不可解绑，请先在上方设置密码。</div>
          )}
        </>
      ) : (
        <Space direction="vertical">
          <Alert type="info" content="当前账号未绑定 APSSO。绑定后可使用 APSSO 统一登录。" />
          <Button long onClick={() => (window.location.href = '/api/v1/auth/oidc/bind/start')}>
            绑定 APSSO
          </Button>
        </Space>
      )}
    </Drawer>
  );
}
