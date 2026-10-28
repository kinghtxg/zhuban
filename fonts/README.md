# fonts/ —— 中文字体目录（不入库）

## 为什么有这个目录

`docker-compose.yml` 把本目录只读挂载到渲染容器的 `/usr/share/fonts/custom`：

```yaml
- ./fonts:/usr/share/fonts/custom:ro
```

LibreOffice 把 docx 转 PDF 时需要中文字体。**字体文件因版权原因不入库**（微软雅黑、宋体等为专有字体），
所以本目录在仓库里只保留这一份说明。

## 部署前必须做的事

从一台 Windows 机器拷贝以下字体到本目录，否则中文 PDF 会回退到容器内置的 Noto CJK
（不会报错，但字形、行高与开发机不一致，可能出现分页错位）：

| 文件名 | 字体 | 用途 |
|---|---|---|
| `msyh.ttc` | 微软雅黑 | 正文默认 |
| `msyhbd.ttc` | 微软雅黑 Bold | 加粗标题 |
| `simsun.ttc` | 宋体 | 公文类模板 |
| `simhei.ttf` | 黑体 | 标题备选 |

来源：`C:\Windows\Fonts\`

```bash
cp /c/Windows/Fonts/{msyh.ttc,msyhbd.ttc,simsun.ttc,simhei.ttf} ./fonts/
```

## 如何确认生效

```bash
docker compose exec render fc-list | grep -i "yahei\|simsun"
```

有输出即挂载成功。

## 与 render-service/fonts 的区别

- `fonts/`（本目录）：**中文字体**，给 LibreOffice 转 PDF 用，不入库
- `render-service/fonts/`：**5 款手写风签名 THIN 字体**（SIL OFL 开源），打包进镜像，已入库

两者用途不同，不要混放。
