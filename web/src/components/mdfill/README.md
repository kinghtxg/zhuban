# mdfill —— 「像写文档一样填写」

把「填写录入」从问卷式表单换成一份带标题的文档：模板字段变成标题，
用户在标题下面写正文，图表处留一个 JSON 代码块。写完回读成 `data`，
走的还是原来那条 `/render`。

## 为什么不是「一套 Markdown 文本编辑器」

用户提的方案是「直接做成 markdown 文档」。实现上**没有引入 Markdown 字符串**
这一层，原因值得写下来，因为它决定了后面所有取舍：

- 铸版的数据模型是 `data: Record<string, unknown>`；参照的那套编辑器数据模型是
  Markdown 文本。它之所以要在中间挂一个 4.35MB 的 Lute 引擎
  （`public/vendor/lute.min.js`，与思源笔记同款），是因为它必须把文档
  **存成 Markdown**。铸版不需要——填完就变成 JSON 了。
- 于是这里直接从 **ProseMirror 文档树**提取数据，少一层「序列化 → 再解析」。
  那一层里每一个规则（空行怎么算、硬折行怎么算、`&` 怎么转义）都是可能丢内容的地方，
  而丢内容正是「格式变了」最常见的来源。

用户要的「像 Markdown 文档一样写」是**观感**，这个观感由 `fillHeading` 节点
（标题条）+ 宽松行距 + 纸张白底提供，不需要真的存 Markdown。

## 与参照编辑器 BlockEditor 的关系

**交互手感是移植的，领域功能全部去掉。** 见 `MarkdownFillEditor.tsx` 里的
扩展注册表——只有 StarterKit（关掉 heading / blockquote）+ Image + FillHeading。

| 参照编辑器有 | 这里 | 理由 |
|---|---|---|
| Tiptap StarterKit、表格、图片、任务列表、代码高亮 | ✅ 保留（表格/任务列表按需） | 写报告用得上 |
| 块操作（Alt+↑↓ 移块、⌘⏎ 下方新起一段） | ⬜ 未移植 | 骨架顺序由模板定，移块没有意义 |
| 斜杠命令菜单 | ⬜ 未移植 | 同上：能插的东西是模板说了算 |
| 查找替换 | ⬜ 未移植 | 报告通常 10 页内，先不做 |
| 双链 `[[标题]]` | ❌ 去掉 | 铸版没有「同书笔记」这个实体 |
| 目录块 `toc-block` | ❌ 去掉 | 目录由 docx 模板自己生成 |
| 白板 / Excalidraw | ❌ 去掉 | 与填表无关 |
| AI 续写 | ❌ 去掉 | 同上 |
| 离线草稿 | ❌ 去掉 | 填写页已有 localStorage 草稿 |
| Lute 引擎（4.35MB） | ❌ 不引入 | 见上一节 |

若日后那边的编辑器改了、想同步手感，对照上表挑要的部分，**不要整包复制**：
那套 BlockEditor 近 2000 行里超过一半是它自己的业务（章节树、乐观锁、wiki 索引）。

## 数据流

```
TemplateVariable[]  ──buildSkeleton──▶  SkeletonNode[]
                                            │
                    已有数据 ────────────────┼──toDocJSON──▶  ProseMirror doc
                                            │
用户编辑 ───────────────────────────────────┘
                                            │
                              extractData ◀─┘
                                    │
                          { data, issues }  ──▶  /validate → /render
```

三个关键约定：

1. **标题是 atom 节点**（`FillHeading.ts`）。没有 content，用户点不进、改不了字。
   做成普通 heading 再靠事务拦截禁止改字，会被中文输入法的组合输入反复误判
   （打「高」时先出现拼音）。`isolating: true` 让退格键不会把两个字段的正文合并。
2. **下标按出现顺序重编号**（`extract.ts`）。用户删掉中间一条后，文档里剩下
   `[0]` 和 `[2]`——照 attrs 提取会让 Jinja 的 `{% for %}` 踩到 undefined，
   整份报告渲染失败。所以 attrs 里的路径只当归属标签。
3. **骨架标题消失的事务一律拒绝**（`FillHeading.ts` 的 `skeletonGuard`）。
   增删条目走 `SKELETON_EDIT_META` 这条专用通路，那不是误删而是产品操作。

## 判据

| 判据 | 在哪 | 证明什么 |
|---|---|---|
| 25 项单测 | `mdfill.test.ts`（`npx vitest run`） | 数据往返恒等：`data → 文档 → data` 逐字段相同；骨架标题全在顶层 |
| 7 组格式不变性 | `verify/mdfill-format-check.py` | 不同数据渲染出的文档，页面/版心/缩进/行距/字体/字号/表格网格逐项一致 |

跑法：

```bash
cd web && npx vitest run                    # 注意：Z: 盘上 vite 会因 UNC 解析失败，
                                            # 先 robocopy 到 C:\tmp\zbuild\web 再跑
cd .. && python verify/mdfill-format-check.py
```

`mdfill-format-check.py` 依赖的样例模板放在**仓库外**的归档目录里。这是硬约束：
由客户内部资料改造出来的模板不进仓库、不进 `//go:embed`、不上线——一进就会跟着
公开快照、Release 附件和容器镜像一起被分发出去。脚本用环境变量
`ZHUBAN_PENTEST_TPL` / `ZHUBAN_PENTEST_DATA` 指定路径。
