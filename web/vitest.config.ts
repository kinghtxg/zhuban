import { defineConfig } from 'vitest/config';
import { fileURLToPath } from 'node:url';

export default defineConfig({
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  test: {
    // 只跑纯逻辑的单测。涉及 DOM 的组件测试走 e2e（Playwright），
    // 这里不引 jsdom —— 填写页的关键风险在数据形状，不在渲染。
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
});
