# docforge 内置签名风字体

本目录字体全部采用 **SIL Open Font License 1.1（OFL 1.1）**，可免费商用，
来源：Google Fonts 仓库（github.com/google/fonts）。

## 字体清单

| 文件 | 字体名（family） | 风格 | 用途 |
| --- | --- | --- | --- |
| MaShanZheng-Regular.ttf | Ma Shan Zheng（马善政毛笔楷书） | brush 毛笔楷书 | 中文签名 |
| LiuJianMaoCao-Regular.ttf | Liu Jian Mao Cao（柳建毛草） | brush 毛草 | 中文签名 |
| ZhiMangXing-Regular.ttf | Zhi Mang Xing（志莽行书） | brush 行书 | 中文签名 |
| LongCang-Regular.ttf | Long Cang（龙藏体） | brush 行书 | 中文签名 |
| DancingScript.ttf | Dancing Script | cursive 英文手写 | 英文签名 |

OFL.txt 为统一许可全文。四款中文字体均已验证覆盖常用简体中文测试集
「示例科技有限公司合同专用章签名测试」，无缺字方框。

## 部署

- 开发/测试：直接读本目录（`render-service/fonts/`）。
- Docker 镜像：Dockerfile 把本目录复制到 `/usr/share/fonts/custom`；
  同时容器内 `fonts-noto-cjk` 提供兜底中文字体。两条路径均由
  `app/fonts.py` 的 FONT_SEARCH_DIRS 按序探测，任一可加载即可。
