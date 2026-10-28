import { useState } from 'react';
import { Alert, Button, Divider, Input, Message, Modal } from '@arco-design/web-react';
import { IconLock, IconUser } from '@arco-design/web-react/icon';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useSearchParams } from 'react-router-dom';
import { devLogin, getSetupStatus, login, register, setupAdmin } from '@/api/endpoints';
import { errorMessage } from '@/utils/format';
import { validatePassword, validateUsername } from '@/utils/validators';
import { formatVersion, useVersion } from '@/hooks/useVersion';

// 登录页（独立全屏路由 /login）
//
// 版式参考「刻本」：左 2/3 深色品牌区 + 右 1/3 白色表单区。
// 两栏各自只回答一个问题——左边「这是什么」，右边「我怎么进去」。
//
// 四种状态都落在右侧同一个容器里，左栏完全不变：
//   - initialized=false  → 首次初始化向导（建首个管理员）
//   - 常规               → 用户名/密码（主路径）
//   - oidc_enabled       → 追加「使用 APSSO 登录」
//   - registration_enabled → 追加注册入口；dev 环境保留一键登录
export default function LoginPage() {
  const qc = useQueryClient();
  const [params] = useSearchParams();
  const redirect = params.get('redirect') || '/';

  const { data: status } = useQuery({
    queryKey: ['setup-status'],
    queryFn: getSetupStatus,
    retry: false,
  });

  const initialized = status?.initialized ?? true;
  const oidcEnabled = status?.oidc_enabled ?? false;
  const registrationEnabled = status?.registration_enabled ?? false;
  const devLoginEnabled = status?.dev_login_enabled ?? false;
  // 页脚版本号（构建期注入）。拿不到就显示空，不编一个。
  const ver = formatVersion(useVersion());

  const [showRegister, setShowRegister] = useState(false);

  const afterLogin = () => {
    qc.invalidateQueries({ queryKey: ['me'] });
    window.location.href = redirect.startsWith('/') ? redirect : '/';
  };

  return (
    <div className="login-split">
      {/* ===== 左栏：品牌区 ===== */}
      <section className="login-brand">
        <div className="login-brand-grid" aria-hidden="true" />
        <div className="login-brand-inner">
          <div className="login-brand-head">
            <span className="brand-mark lg">铸</span>
            {/* 字标按刻本的做法：中文名 + 拼音小字，两行锁成一个整体。
                docforge 保留为产品 id（仓库/二进制/接口文档），不进界面。 */}
            <span className="login-wordmark">
              <b>铸版</b>
              <i>ZHUBAN</i>
            </span>
          </div>

          <div className="login-hero">
            <h1 className="login-title">
              版式只需要定一次，
              <br />
              之后每一份都是<em>成品</em>。
            </h1>
            <p className="login-lead">
              上传 Word 模板，用占位符标出要替换的位置；之后填一份 JSON 或对接接口，
              系统按模板渲染、编号、套签章，直接产出可下发的文档。
            </p>
          </div>

          <ul className="login-points">
            <li>
              <span>01</span>
              <b>定</b>上传 .docx，占位符自动识别
            </li>
            <li>
              <span>02</span>
              <b>填</b>JSON 或接口传参，字段级校验
            </li>
            <li>
              <span>03</span>
              <b>出</b>渲染编号套章，一次批量下发
            </li>
          </ul>

          <div className="login-brand-foot">
            <span>© 2026 铸版 ZHUBAN · Apache-2.0</span>
            {/* 版本取构建期注入值；拿不到就留空，不写死一个会过期的常量 */}
            <span>{ver || ''}</span>
          </div>
        </div>
      </section>

      {/* ===== 右栏：登录区 ===== */}
      <aside className="login-panel">
        <div className={`login-panel-inner${initialized ? '' : ' is-tall'}`}>
          <div className="login-panel-head">
            <h2>{initialized ? '登录' : '初始化'}</h2>
            <p>
              {initialized ? '使用账号密码继续' : '系统尚未初始化，请创建第一个管理员账号'}
            </p>
          </div>

          {!initialized ? (
            <SetupForm onDone={afterLogin} />
          ) : (
            <>
              <LoginForm oidcEnabled={oidcEnabled} onSuccess={afterLogin} />

              {registrationEnabled && (
                <div className="login-subentry">
                  <Button type="text" size="small" onClick={() => setShowRegister(true)}>
                    没有账号？申请注册（需管理员审核）
                  </Button>
                </div>
              )}

              {devLoginEnabled && (
                <>
                  <Divider style={{ color: 'var(--color-text-4)', fontSize: 12 }}>开发环境</Divider>
                  <div style={{ textAlign: 'center' }}>
                    <Button
                      type="text"
                      size="mini"
                      onClick={() =>
                        devLogin('dev')
                          .then(afterLogin)
                          .catch(() => Message.warning('当前环境未开放开发一键登录'))
                      }
                    >
                      开发一键登录
                    </Button>
                  </div>
                </>
              )}
            </>
          )}

          <div className="login-panel-foot">
            铸版 ZHUBAN
            <br />
            私有部署的 Word 模板渲染平台
          </div>
        </div>
      </aside>

      {showRegister && (
        <RegisterModal
          onClose={() => setShowRegister(false)}
          onDone={() => {
            setShowRegister(false);
            Message.success('注册成功，请等待管理员审核后登录');
          }}
        />
      )}
    </div>
  );
}

// ---------- 常规登录 ----------
// 字段不用 Form.Item 的标签：登录表单只有两个字段，标签纯属噪音，
// 用前置图标 + 占位符表达即可（同时补 aria-label 保证读屏可用）。
function LoginForm({ oidcEnabled, onSuccess }: { oidcEnabled: boolean; onSuccess: () => void }) {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');

  const mutation = useMutation({
    mutationFn: () => login({ username: username.trim(), password }),
    onSuccess,
  });

  const err = mutation.error;
  const { summary, details } = err ? errorMessage(err) : { summary: '', details: [] };

  const submit = () => {
    if (!username.trim() || !password) {
      Message.warning('请输入用户名和密码');
      return;
    }
    mutation.mutate();
  };

  return (
    <div>
      {err && (
        <Alert
          style={{ marginBottom: 12 }}
          type="error"
          content={
            <div>
              <div>{summary}</div>
              {details.length > 0 && (
                <div style={{ fontSize: 12, marginTop: 4 }}>{details.join('；')}</div>
              )}
            </div>
          }
        />
      )}

      <div className="login-fields">
        <Input
          size="large"
          prefix={<IconUser />}
          placeholder="用户名"
          aria-label="用户名"
          value={username}
          onChange={setUsername}
          onPressEnter={submit}
          autoFocus
        />
        <Input.Password
          size="large"
          prefix={<IconLock />}
          placeholder="密码"
          aria-label="密码"
          value={password}
          onChange={setPassword}
          onPressEnter={submit}
        />
      </div>

      <div className="login-actions">
        <Button
          type="primary"
          long
          className="login-primary"
          loading={mutation.isPending}
          onClick={submit}
        >
          登录
        </Button>
      </div>

      {oidcEnabled && (
        <>
          <Divider style={{ color: 'var(--color-text-4)', fontSize: 12 }}>或</Divider>
          <Button
            long
            style={{ height: 40, borderRadius: 9 }}
            onClick={() => (window.location.href = '/api/v1/auth/oidc/start')}
          >
            使用 APSSO 统一登录
          </Button>
        </>
      )}
    </div>
  );
}

// ---------- 首次初始化向导 ----------
function SetupForm({ onDone }: { onDone: () => void }) {
  const [username, setUsername] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');

  const mutation = useMutation({
    mutationFn: () =>
      setupAdmin({
        username: username.trim(),
        password,
        display_name: displayName.trim() || undefined,
      }),
    onSuccess: onDone,
  });

  const { summary, details } = mutation.error
    ? errorMessage(mutation.error)
    : { summary: '', details: [] };

  return (
    <div>
      {mutation.error && (
        <Alert
          style={{ marginBottom: 12 }}
          type="error"
          content={`${summary}${details.length ? '（' + details.join('；') + '）' : ''}`}
        />
      )}
      <div className="login-fields">
        <Input
          size="large"
          prefix={<IconUser />}
          placeholder="管理员用户名（3-64 位字母/数字/下划线）"
          aria-label="管理员用户名"
          value={username}
          onChange={setUsername}
        />
        <Input
          size="large"
          placeholder="显示名（可选，如：管理员）"
          aria-label="显示名"
          value={displayName}
          onChange={setDisplayName}
        />
        <Input.Password
          size="large"
          prefix={<IconLock />}
          placeholder="密码（至少 8 位，含字母和数字）"
          aria-label="密码"
          value={password}
          onChange={setPassword}
        />
        <Input.Password
          size="large"
          prefix={<IconLock />}
          placeholder="再次输入密码"
          aria-label="确认密码"
          value={confirm}
          onChange={setConfirm}
        />
      </div>
      <div className="login-actions">
        <Button
          type="primary"
          long
          className="login-primary"
          loading={mutation.isPending}
          onClick={() => {
            const uerr = validateUsername(username.trim());
            if (uerr) return Message.warning(uerr);
            const perr = validatePassword(password);
            if (perr) return Message.warning(perr);
            if (password !== confirm) return Message.warning('两次输入的密码不一致');
            mutation.mutate();
          }}
        >
          创建管理员并进入
        </Button>
      </div>
    </div>
  );
}

// ---------- 注册弹窗 ----------
function RegisterModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [username, setUsername] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');

  const mutation = useMutation({
    mutationFn: () =>
      register({
        username: username.trim(),
        password,
        email: email.trim() || undefined,
        display_name: displayName.trim() || undefined,
      }),
    onSuccess: onDone,
    onError: (err) => {
      const { summary, details } = errorMessage(err);
      Message.error({ content: `${summary}${details.length ? '（' + details.join('；') + '）' : ''}` });
    },
  });

  return (
    <Modal title="申请注册" visible onCancel={onClose} footer={null} unmountOnExit>
      <div className="login-fields" style={{ marginTop: 8 }}>
        <Input
          size="large"
          prefix={<IconUser />}
          placeholder="用户名（3-64 位字母/数字/下划线）"
          aria-label="用户名"
          value={username}
          onChange={setUsername}
        />
        <Input
          size="large"
          placeholder="显示名（可选）"
          aria-label="显示名"
          value={displayName}
          onChange={setDisplayName}
        />
        <Input
          size="large"
          placeholder="邮箱（可选，用于 APSSO 账号关联）"
          aria-label="邮箱"
          value={email}
          onChange={setEmail}
        />
        <Input.Password
          size="large"
          prefix={<IconLock />}
          placeholder="密码（至少 8 位，含字母和数字）"
          aria-label="密码"
          value={password}
          onChange={setPassword}
        />
      </div>
      <div className="login-actions">
        <Button
          type="primary"
          long
          className="login-primary"
          loading={mutation.isPending}
          onClick={() => {
            const uerr = validateUsername(username.trim());
            if (uerr) return Message.warning(uerr);
            const perr = validatePassword(password);
            if (perr) return Message.warning(perr);
            mutation.mutate();
          }}
        >
          提交注册申请
        </Button>
      </div>
    </Modal>
  );
}
