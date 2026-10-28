import { usePagination } from '@/hooks/usePagination';
import { useState } from 'react';
import {
  Button,
  Dropdown,
  Form,
  Input,
  Menu,
  Message,
  Modal,
  Popconfirm,
  Select,
  Table,
  Tag,
} from '@arco-design/web-react';
import { IconMore, IconPlus, IconRefresh } from '@arco-design/web-react/icon';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  approveUser,
  changeUserRole,
  createAdminUser,
  disableUser,
  enableUser,
  listAdminUsers,
  resetUserPassword,
} from '@/api/endpoints';
import type { Role, User } from '@/types/api';
import { lookupMeta } from '@/constants/enums';
import { errorMessage, fmtTime } from '@/utils/format';
import { validatePassword, validateUsername } from '@/utils/validators';
import PageContainer from '@/components/PageContainer';

const STATUS_META: Record<User['status'], { label: string; color: string }> = {
  pending: { label: '待审核', color: 'orange' },
  active: { label: '正常', color: 'green' },
  disabled: { label: '已禁用', color: 'red' },
};

export default function UsersAdminPage({ currentUserId }: { currentUserId: string }) {
  const qc = useQueryClient();
  const [q, setQ] = useState('');
  const [status, setStatus] = useState('');
  const [source, setSource] = useState('');
  const { page, setPage, pagination } = usePagination();
  const [createOpen, setCreateOpen] = useState(false);
  const [resetTarget, setResetTarget] = useState<User | null>(null);

  const { data, isLoading } = useQuery({
    queryKey: ['admin-users', { q, status, source, page }],
    queryFn: () => listAdminUsers({ q: q || undefined, status: status || undefined, source: source || undefined, page, page_size: 20 }),
  });

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ['admin-users'] });
    qc.invalidateQueries({ queryKey: ['admin-users-pending'] });
  };

  const act = (fn: () => Promise<unknown>, ok: string) =>
    fn()
      .then(() => {
        Message.success(ok);
        refresh();
      })
      .catch((err) => Message.error(errorMessage(err).summary));

  const columns = [
    { title: '用户名', dataIndex: 'username', width: 140 },
    { title: '显示名', dataIndex: 'display_name', width: 110, render: (v: string) => v || '—' },
    { title: '邮箱', dataIndex: 'email', render: (v: string) => v || '—' },
    {
      title: '角色',
      dataIndex: 'role',
      width: 90,
      render: (role: Role) => (
        <Tag size="small" color={role === 'admin' ? 'orange' : 'gray'}>
          {role === 'admin' ? '管理员' : '成员'}
        </Tag>
      ),
    },
    {
      title: '状态',
      dataIndex: 'status',
      width: 88,
      render: (s: User['status']) => {
        const m = lookupMeta(STATUS_META, s);
        return <Tag size="small" color={m.color}>{m.label}</Tag>;
      },
    },
    {
      title: '来源',
      dataIndex: 'source',
      width: 88,
      render: (s: User['source']) => <Tag size="small">{s === 'local' ? '本地' : 'APSSO'}</Tag>,
    },
    {
      title: 'APSSO',
      dataIndex: 'oidc_bound',
      width: 88,
      render: (v: boolean) => (v ? <Tag size="small" color="green">已绑定</Tag> : <Tag size="small">未绑定</Tag>),
    },
    {
      title: '最后登录',
      dataIndex: 'last_login_at',
      width: 150,
      render: (v: string) => <span style={{ whiteSpace: 'nowrap' }}>{v ? fmtTime(v) : '从未登录'}</span>,
    },
    {
      title: '操作',
      dataIndex: 'actions',
      width: 130,
      fixed: 'right' as const,
      render: (_: unknown, record: User) => {
        const isSelf = record.id === currentUserId;

        // 待审核：直接给出通过 / 拒绝，不做二次确认（可逆且高频）
        if (record.status === 'pending') {
          return (
            <div className="cell-actions">
              <Button size="small" type="primary" onClick={() => act(() => approveUser(record.id), '已通过审核')}>
                通过
              </Button>
              <Button size="small" status="danger" onClick={() => act(() => disableUser(record.id), '已拒绝')}>
                拒绝
              </Button>
            </div>
          );
        }

        // 其余状态：低频管理动作收进「更多」，改角色需二次确认
        return (
          <div className="cell-actions">
            <Dropdown
              droplist={
                <Menu
                  onClickMenuItem={(key: string) => {
                    if (key === 'toggle') {
                      act(
                        () => (record.status === 'active' ? disableUser(record.id) : enableUser(record.id)),
                        record.status === 'active' ? '已禁用' : '已启用',
                      );
                    } else if (key === 'toAdmin' || key === 'toMember') {
                      const next: Role = key === 'toAdmin' ? 'admin' : 'member';
                      Modal.confirm({
                        title: '变更角色',
                        content: `确认将「${record.display_name || record.username}」设为${next === 'admin' ? '管理员' : '成员'}？管理员可管理用户、审计日志与系统设置。`,
                        okText: '确认变更',
                        onOk: () => act(() => changeUserRole(record.id, next), '角色已更新'),
                      });
                    } else if (key === 'reset') {
                      setResetTarget(record);
                    }
                  }}
                >
                  <Menu.Item key="reset">重置密码</Menu.Item>
                  <Menu.Item key={record.role === 'admin' ? 'toMember' : 'toAdmin'}>
                    {record.role === 'admin' ? '设为成员' : '设为管理员'}
                  </Menu.Item>
                  <Menu.Item key="toggle" disabled={isSelf}>
                    {record.status === 'active' ? '禁用账号' : '启用账号'}
                  </Menu.Item>
                </Menu>
              }
            >
              <Button size="small" icon={<IconMore />} aria-label="更多操作" />
            </Dropdown>
          </div>
        );
      },
    },
  ];

  return (
    <PageContainer
      title="用户管理"
      subtitle="账号审核、角色与状态管理（仅管理员可见）"
      extra={
        <>
          <Button icon={<IconRefresh />} onClick={refresh}>
            刷新
          </Button>
          <Button type="primary" icon={<IconPlus />} onClick={() => setCreateOpen(true)}>
            新建用户
          </Button>
        </>
      }
      toolbar={
        <>
          <Input.Search
            placeholder="用户名 / 显示名 / 邮箱"
            style={{ width: 260 }}
            onSearch={(v) => {
              setQ(v);
              setPage(1);
            }}
          />
          <Select
            placeholder="状态：全部"
            style={{ width: 130 }}
            allowClear
            onChange={(v) => {
              setStatus(v || '');
              setPage(1);
            }}
          >
            <Select.Option value="pending">待审核</Select.Option>
            <Select.Option value="active">正常</Select.Option>
            <Select.Option value="disabled">已禁用</Select.Option>
          </Select>
          <Select
            placeholder="来源：全部"
            style={{ width: 130 }}
            allowClear
            onChange={(v) => {
              setSource(v || '');
              setPage(1);
            }}
          >
            <Select.Option value="local">本地</Select.Option>
            <Select.Option value="oidc">APSSO</Select.Option>
          </Select>
        </>
      }
    >
      <div className="data-card">
        <Table
          rowKey="id"
          loading={isLoading}
          columns={columns}
          data={data?.items || []}
          scroll={{ x: 1010 }}
          pagination={pagination(data?.total)}
          noDataElement={<div className="empty-block empty-block-desc">没有匹配的用户</div>}
        />
      </div>

      {createOpen && (
        <CreateUserModal
          onClose={() => setCreateOpen(false)}
          onDone={() => {
            setCreateOpen(false);
            refresh();
          }}
        />
      )}
      {resetTarget && (
        <ResetPasswordModal
          user={resetTarget}
          onClose={() => setResetTarget(null)}
          onDone={() => {
            setResetTarget(null);
            refresh();
          }}
        />
      )}
    </PageContainer>
  );
}

// ---------- 新建用户 ----------
function CreateUserModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [username, setUsername] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [role, setRole] = useState<Role>('member');

  const mutation = useMutation({
    mutationFn: () =>
      createAdminUser({
        username: username.trim(),
        password,
        display_name: displayName.trim() || undefined,
        email: email.trim() || undefined,
        role,
      }),
    onSuccess: () => {
      Message.success('用户已创建');
      onDone();
    },
    onError: (err) => Message.error(errorMessage(err).summary),
  });

  return (
    <Modal title="新建用户" visible onCancel={onClose} footer={null} unmountOnExit>
      <Form layout="vertical">
        <Form.Item label="用户名" required>
          <Input value={username} onChange={setUsername} />
        </Form.Item>
        <Form.Item label="显示名">
          <Input value={displayName} onChange={setDisplayName} />
        </Form.Item>
        <Form.Item label="邮箱">
          <Input value={email} onChange={setEmail} />
        </Form.Item>
        <Form.Item label="初始密码" required>
          <Input.Password value={password} onChange={setPassword} placeholder="至少 8 位，须含字母和数字" />
        </Form.Item>
        <Form.Item label="角色" required>
          <Select value={role} onChange={(v) => setRole(v as Role)} style={{ width: '100%' }}>
            <Select.Option value="admin">管理员</Select.Option>
            <Select.Option value="member">成员</Select.Option>
          </Select>
        </Form.Item>
        <Button
          type="primary"
          long
          loading={mutation.isPending}
          onClick={() => {
            const uerr = validateUsername(username.trim());
            if (uerr) return Message.warning(uerr);
            const perr = validatePassword(password);
            if (perr) return Message.warning(perr);
            mutation.mutate();
          }}
        >
          创建
        </Button>
      </Form>
    </Modal>
  );
}

// ---------- 重置密码 ----------
function ResetPasswordModal({ user, onClose, onDone }: { user: User; onClose: () => void; onDone: () => void }) {
  const [password, setPassword] = useState('');
  const mutation = useMutation({
    mutationFn: () => resetUserPassword(user.id, password),
    onSuccess: () => {
      Message.success(`已重置 ${user.username} 的密码`);
      onDone();
    },
    onError: (err) => Message.error(errorMessage(err).summary),
  });
  return (
    <Modal title={`重置密码：${user.username}`} visible onCancel={onClose} footer={null} unmountOnExit>
      <Form layout="vertical">
        <Form.Item label="新密码" required>
          <Input.Password value={password} onChange={setPassword} placeholder="至少 8 位，须含字母和数字" />
        </Form.Item>
        <Popconfirm title="确认重置？用户下次登录需使用新密码。">
          <Button type="primary" long loading={mutation.isPending} onClick={() => {
            const perr = validatePassword(password);
            if (perr) return Message.warning(perr);
            mutation.mutate();
          }}>
            确认重置
          </Button>
        </Popconfirm>
      </Form>
    </Modal>
  );
}
