# 审计模板迁移验证记录（MIGRATION-VERIFY）

> 由 render-service 在本机真实渲染生成。验证对象：`samples/templates/audit-report.docx` + `samples/audit-report.sample.json`。
> 渲染产物留存：`samples/_verify/audit-report.rendered.docx`。

## 1. 迁移方法

1. 复制只读的 `code-audit-report/模板.docx` 到 `samples/templates/audit-report.docx`，在副本上做 XML 级改造（lxml），不重建表格。
2. 跨 run 预处理：把 `{{被测件版本}}`（原拆 3 run）、`TI-{{项目编号}}-DMA002`（原拆 5 run）等被 Word 拆 run 的占位符合并为单 run。
3. 循环字段重命名：`{{name}}→{{item.name}}`、`{{缺陷等级}}→{{item.缺陷等级}}`、`{{次数}}→{{item.次数}}`、`{{description}}→{{item.description}}`、`{{缺陷说明}}→{{item.缺陷说明}}`、`{{mitigation}}→{{item.mitigation}}`；封面字段 `{{被测件名称}}/{{被测件版本}}/{{项目编号}}` 保持顶层。
4. 段落级循环：`{%p for item in items %}` 包裹「附录 numPr 段落 + 整张大表」；表间 3 个空段落用 `{%p if not loop.last %}`/`{%p endif %}` 包裹（最后一张表后不留多余空白段）。
5. 签字单元格：表前部第一次出现的「测评人员 / 日期」空单元格填入 `{{测试人员}}` / `{{测试时间}}`。
6. 原模板全部样式（字体、边框、gridSpan/vMerge、numbering.xml、numPr 附录自动编号）原样保留。

## 2. 本机真实渲染证据（示例 JSON = 3 条 items）

| 检查项 | 结果 | 期望 |
| --- | --- | --- |
| 残留 `{{` | 0 | 0 |
| 残留 `{%` | 0 | 0 |
| 表格 `<w:tbl>` 数量 | 3 | == items 数 = 3 |
| body 直属附录 numPr 段落数 | 3 | == items 数 = 3（附录 A/B/C） |
| 富文本加粗 `<w:b>` | 3 | >0 |
| 列表符 `•` | 存在 | 存在 |
| 封面字段渲染 | 被测件名称/版本/项目编号/测试人员/时间均正确 | 全部渲染 |

说明：原始模板的表格内本就存在 1 个 numPr 段落（表格自带样式），渲染后随每张表正确重复；body 直属的附录编号段落才是验收口径中的「附录 A/B/C」，其数量 == items 数 == 3。

## 3. 图片 / 二维码夹具验证

用独立测试模板（含 `{{qr_x}}` 与 data-URI 图片）渲染：

```
新增 media 部件: ['word/media/image1.png', 'word/media/image2.png']
media 数量: 2（二维码 1 + 图片 1）
```

## 4. 未验证项（需 Docker / LibreOffice 环境）

- `format=pdf` 转换、`/preview` 逐页 PNG + 水印：本机无 LibreOffice / poppler，`/healthz` 返回 `libreoffice:false, poppler:false`，相关端点返回 502。Dockerfile 内已安装 `libreoffice-writer`、`poppler-utils`、`fonts-noto-cjk(-extra)`，容器内应可用；pytest 中 pdf/预览用例在缺 soffice 时按契约 skip。
