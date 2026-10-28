import { useEffect, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { getUpdates } from '@/api/endpoints';
import './update-notice.css';

/**
 * 右下角「有新版本」提示。
 *
 * 设计口径（三条，改任何一条都会让这个功能变成打扰）：
 *
 * 1. **只在真的能升级时出现**。has_update=false、stale=true（没查成）、
 *    dev=true（本地构建）一律不弹。一个不知道自己是不是最新的提示，
 *    除了制造焦虑没有别的作用。
 *
 * 2. **不自动升级、不催**。只给「查看更新」开个新标签页指向发布说明。
 *    服务器上自动替换二进制是最容易把服务搞挂的一类功能（下到一半、
 *    权限不对、正在跑的任务被中断），这里不做。
 *
 * 3. **两级关闭**。「稍后」只压住本次会话，「忽略此版本」写进 localStorage
 *    并且按版本号记——下一个版本发布时还会再提醒一次，不会永久静音。
 *    这也是为什么 key 里带 latest 而不是一个布尔值。
 *
 * 挂在 main.tsx 里而不是 App 里：App 在未登录时会整棵替换成登录页，
 * 挂在那儿的话登录页就没有升级提示，而自建用户恰恰常常卡在登录页。
 */

const IGNORED_KEY = 'zb:update-ignored';
const SNOOZE_KEY = 'zb:update-snoozed';

function readStore(kind: 'local' | 'session', key: string): string {
  try {
    return (kind === 'local' ? localStorage : sessionStorage).getItem(key) || '';
  } catch {
    // 隐私模式下 storage 会抛异常。读不到就当没记过——宁可多提醒一次，
    // 也不要因为一个 try 让整个组件崩掉。
    return '';
  }
}

function writeStore(kind: 'local' | 'session', key: string, value: string) {
  try {
    (kind === 'local' ? localStorage : sessionStorage).setItem(key, value);
  } catch {
    /* 同上：写不进去只是"下次还会提醒"，不是错误 */
  }
}

export default function UpdateNotice() {
  // 首屏之后再去查：这个请求要打外网（服务端代查 GitHub），
  // 和登录、列表这些必须立刻发出的请求抢带宽没有意义。
  const [armed, setArmed] = useState(false);
  const [closed, setClosed] = useState(false);

  useEffect(() => {
    const t = window.setTimeout(() => setArmed(true), 2500);
    return () => window.clearTimeout(t);
  }, []);

  const { data } = useQuery({
    queryKey: ['updates'],
    queryFn: () => getUpdates(),
    enabled: armed,
    // 服务端自己有 6h 缓存，前端不必再查第二遍；切页面、切窗口都不重查。
    staleTime: Infinity,
    refetchOnMount: false,
    refetchOnWindowFocus: false,
    retry: false,
  });

  const latest = data?.latest || '';
  const visible = useMemo(() => {
    if (closed || !data || !data.has_update || data.dev || !latest) return false;
    const ignored = readStore('local', IGNORED_KEY);
    if (ignored && ignored === latest) return false;
    const snoozed = readStore('session', SNOOZE_KEY);
    if (snoozed && snoozed === latest) return false;
    return true;
  }, [closed, data, latest]);

  if (!visible || !data) return null;

  const notes = (data.notes || '').replace(/\r/g, '').trim();

  const openRelease = () => {
    if (!data.url) return;
    // noopener：新标签页拿不到 window.opener，避免它反过来操作本页
    window.open(data.url, '_blank', 'noopener,noreferrer');
    // 点过「查看更新」就不必再挂着这张卡了
    writeStore('session', SNOOZE_KEY, latest);
    setClosed(true);
  };

  return (
    <aside className="un-card" role="status" aria-live="polite" aria-label="有新版本可用">
      <button
        className="un-close"
        aria-label="关闭"
        onClick={() => {
          writeStore('session', SNOOZE_KEY, latest);
          setClosed(true);
        }}
      >
        ×
      </button>

      <div className="un-head">
        <span className="un-mark" aria-hidden="true">
          <svg viewBox="0 0 24 24" width="15" height="15">
            <path
              d="M12 19V6M12 6l-5 5M12 6l5 5"
              fill="none"
              stroke="currentColor"
              strokeWidth="2.1"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
        </span>
        <div>
          <p className="un-title">有新版本可用</p>
          <p className="un-ver">
            <span>{data.current.startsWith('v') ? data.current : `v${data.current}`}</span>
            <i aria-hidden="true">→</i>
            <b>{latest}</b>
          </p>
        </div>
      </div>

      {notes && <p className="un-notes">{notes}</p>}

      <div className="un-actions">
        <button className="un-btn un-btn-primary" onClick={openRelease} disabled={!data.url}>
          查看更新
        </button>
        <button
          className="un-btn un-btn-text"
          onClick={() => {
            writeStore('local', IGNORED_KEY, latest);
            setClosed(true);
          }}
          title={`不再提示 ${latest}（新版本发布时仍会提醒）`}
        >
          忽略此版本
        </button>
      </div>
    </aside>
  );
}
