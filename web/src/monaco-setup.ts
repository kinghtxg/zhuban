/**
 * Monaco 本地打包的唯一初始化点。
 *
 * 为什么它不在 main.tsx 里：这段代码 `import * as monaco from 'monaco-editor'`
 * 是把整个编辑器打进包的入口。它原来写在入口文件里，于是**任何人打开登录页**
 * 都要先下载完 Monaco 才能看到表单——包括从来不用编辑器的人。
 * 现在这段逻辑只被真正用编辑器的两个页面（模板试运行 / 投递调试台）引入，
 * 那两个页面又是按需加载的，Monaco 就成了点开才下载的独立分块。
 *
 * 注意：CONTRACT §7 要求禁止运行时从 CDN 拉 Monaco。这里 `loader.config({ monaco })`
 * 把本地实例注入 @monaco-editor/react，任何情况下都不会走 jsdelivr；
 * vite.config.ts 里另有一个插件在构建期抹掉库内自带的 CDN 字面量。
 */
import * as monaco from 'monaco-editor';
import { loader } from '@monaco-editor/react';
import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker';
import JsonWorker from 'monaco-editor/esm/vs/language/json/json.worker?worker';

(self as unknown as { MonacoEnvironment: monaco.Environment }).MonacoEnvironment = {
  getWorker(_workerId: string, label: string) {
    if (label === 'json') return new JsonWorker();
    return new EditorWorker();
  },
};

loader.config({ monaco });
