#!/bin/sh
# 铸版渲染服务入口。
#
# 这里只做一件事：**在字体已经挂载进来之后**决定要不要补中文族名映射，然后降权。
#
# 为什么不能放在镜像构建期：容器里的中文正版字体（simsun/simhei/msyh）是运行时
# 从宿主机挂进 /usr/share/fonts/custom 的，构建期那个目录是空的。构建期写一份
# 「宋体 → Noto Serif CJK SC」的配置，启动后就会把挂进来的正版宋体抢走 ——
# 而且不报错，只有对比 PDF 字形才能发现。
#
# 所以：以 root 跑一次 --install（它先 fc-list 查本机有没有，有就跳过），
# 再 setpriv 降到 appuser 跑服务。服务进程本身始终是非 root。
set -e

if [ "$(id -u)" = "0" ]; then
    python -m app.cjk_fonts --install || true
    exec setpriv --reuid=appuser --regid=appuser --init-groups "$@"
fi

exec "$@"
