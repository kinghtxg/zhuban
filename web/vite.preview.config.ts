/**
 * 临时预览配置 —— 仅用于本地查看布局效果
 * 与主配置唯一差异：/api 代理指向一个已部署的实例（默认 http://127.0.0.1:18100，
 * 可用 ZB_PREVIEW_TARGET 覆盖），
 * 避免本地需另起 Go 后端。确认效果后可删除本文件。
 */
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { fileURLToPath, URL } from 'node:url';

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    host: '127.0.0.1',
    proxy: {
      '/api': {
        target: process.env.ZB_PREVIEW_TARGET || 'http://127.0.0.1:18100',
        changeOrigin: true,
      },
    },
  },
});
