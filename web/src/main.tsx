import React from 'react';
import ReactDOM from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ConfigProvider } from '@arco-design/web-react';
import zhCN from '@arco-design/web-react/es/locale/zh-CN';
import '@arco-design/web-react/dist/css/arco.css';
import './styles/global.css';

/**
 * Arco 主题 —— 全站只在这里下发一次。
 *
 * ⚠️ primaryColor 必须传十六进制字面量（务必不要回退）：
 * 曾写作 primaryColor: 'rgb(var(--primary-6))'，ConfigProvider 拿到后要按 HSL
 * 推导 1~10 号色阶，正则解析不出 RGB，就把 body 上的 --primary-6 覆写成
 * "NaN,NaN,NaN"，全站 rgb(var(--primary-6)) 一起失效：主按钮变白底、
 * 品牌方块渐变消失（白字白底看不见）、选中态掉色。
 *
 * 取值 = Arco 默认 arcoblue-6。与「刻本」一致：本产品不改主色，
 * 主色交给 Arco 默认值，品牌识别靠 --brand-gradient（蓝紫渐变）承担。
 *
 * borderRadius: 6 —— 控件级圆角（按钮/输入框/下拉）。面板级 10px 不走这里，
 * 由 tokens.css 的 --app-radius-panel 单独控制，两者是不同层级，不能合并。
 *
 * locale: zhCN —— 此前漏配，导致空状态、分页器、日期选择器这些
 * Arco 内置文案全是英文，在中文界面里非常突兀。
 */
const ARCO_THEME = {
  primaryColor: '#165DFF',
  successColor: '#00B42A',
  warningColor: '#FF7D00',
  dangerColor: '#F53F3F',
  borderRadius: 6,
};

import App from './App';
import UpdateNotice from './components/UpdateNotice';

/**
 * 发版之后，用户浏览器里还开着旧页面：此时按需加载的分块文件名已经不存在，
 * 动态 import 会拿到 404 / HTML，页面就永久停在骨架屏上。
 *
 * Vite 会把这类失败抛成 vite:preloadError，这里整页刷新一次即可拿到新的
 * index.html。加时间窗是因为「新版本自己也缺分块」时（发布中断、CDN 只同步了一半）
 * 无条件重载会变成死循环，而一个打不开的页面比一个可以手动刷新的页面更难排查。
 */
const RELOAD_KEY = 'zb:preload-reload-at';
window.addEventListener('vite:preloadError', (e) => {
  e.preventDefault();
  const last = Number(sessionStorage.getItem(RELOAD_KEY) || 0);
  if (Date.now() - last < 30_000) return; // 30 秒内只自救一次
  sessionStorage.setItem(RELOAD_KEY, String(Date.now()));
  window.location.reload();
});

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: false,
      refetchOnWindowFocus: false,
      staleTime: 10_000,
    },
  },
});

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ConfigProvider theme={ARCO_THEME} locale={zhCN}>
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <App />
          {/*
            升级提示挂在 App 外面：App 在未登录时整棵替换成登录页，
            挂进去的话登录页就没有提示了——而自建用户最常停的正是登录页。
            它自己也只在「真的能升级」时渲染（口径见组件注释）。
          */}
          <UpdateNotice />
        </BrowserRouter>
      </QueryClientProvider>
    </ConfigProvider>
  </React.StrictMode>,
);
