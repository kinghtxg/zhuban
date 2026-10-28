import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';
import { fileURLToPath, URL } from 'node:url';

// 构建后清理 monaco loader 的默认 CDN 字面量（运行时已 loader.config({ monaco }) 本地注入，
// 该默认值永不执行，但静态扫描 dist 时会命中 jsdelivr，这里构建期直接抹掉，保证产物零外链。
function stripMonacoCdn(): Plugin {
  const CDN = 'https://cdn.jsdelivr.net/npm/monaco-editor@0.55.1/min/vs';
  return {
    name: 'strip-monaco-cdn',
    renderChunk(code) {
      if (code.includes(CDN)) return code.split(CDN).join('');
      return code;
    },
  };
}

// docforge 前端构建配置：
// - dev server 5173，/api 代理到 Go 主服务 8100
// - Monaco 全部本地打包（?worker），禁止运行时从 CDN 拉取，保证内网断网可用
export default defineConfig({
  plugins: [react(), stripMonacoCdn()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        // 本地联调时后端端口可覆盖：DOCFORGE_API=http://127.0.0.1:18100 npm run dev
        target: process.env.DOCFORGE_API || 'http://localhost:8100',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 4096,
    rollupOptions: {
      output: {
        // 拆块的依据是「谁会变」，不是「谁大」：
        //   - vendor：react / arco / 路由这些第三方库，只在升级依赖时变，
        //     单独成块后它们能在多次发版之间一直命中浏览器缓存；
        //   - monaco：编辑器，只有模板试运行与投递调试台会加载，
        //     配合 App.tsx 里的 lazy() 变成「点开才下载」；
        //   - 其余（业务代码）留在入口块，每次发版都会变，不值得为它做缓存优化。
        // 之前这里是单块 4.5MB：登录页也要先下完整个 Monaco 才能渲染表单。
        manualChunks(id: string) {
          const p = id.replace(/\\/g, '/');
          if (!p.includes('/node_modules/')) return;
          // monaco 故意不参与手动分块，交给 Rollup 按动态 import 自动切。
          // 踩过一次：把它钉成独立 chunk 之后，Rollup 会把这个 chunk 当成
          // 共享依赖的落脚点（入口只用到其中一个 helper），结果 index.html
          // 里出现 modulepreload monaco —— 3.9MB 照样在首屏下载，白拆。
          if (p.includes('/monaco-editor/')) return;
          return 'vendor';
        },
      },
    },
  },
});
