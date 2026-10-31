# docforge render-service: FastAPI + docxtpl + LibreOffice + poppler
FROM python:3.12-slim

ENV PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    PORT=8101 \
    TMP_DIR=/tmp/docforge

# 构建源址。**默认走官方源**，因为这是开源镜像：写死某个云厂商的内网源，
# 等于让全世界非该云的用户构建失败（而且失败得很慢——apt 会重试到五分钟以上）。
#
# 中国大陆/腾讯云的用户请覆盖成内网源，构建时间能差一个量级：
#   --build-arg APT_MIRROR=mirrors.tencentyun.com \
#   --build-arg PIP_INDEX=https://mirrors.tencentyun.com/pypi/simple
# docker compose 里已经接好了这两个变量，写在 .env 即可。
ARG APT_MIRROR=deb.debian.org
ARG PIP_INDEX=https://pypi.org/simple

# Debian 12+ 的源可能在 /etc/apt/sources.list，也可能是 sources.list.d 下的 deb822 格式，
# 两种都要覆盖，只改一处会在新基础镜像上静默失效。
RUN set -eux; \
    for f in /etc/apt/sources.list /etc/apt/sources.list.d/*.sources /etc/apt/sources.list.d/*.list; do \
        if [ -f "$f" ]; then \
            sed -i "s|http://deb.debian.org|http://${APT_MIRROR}|g; \
                    s|http://security.debian.org|http://${APT_MIRROR}|g; \
                    s|https://deb.debian.org|http://${APT_MIRROR}|g" "$f"; \
        fi; \
    done; \
    apt-get update && apt-get install -y --no-install-recommends \
        libreoffice-writer \
        poppler-utils \
        fonts-noto-cjk \
        fonts-dejavu \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /srv/render-service

COPY requirements.txt .
RUN pip install --no-cache-dir -i "$PIP_INDEX" -r requirements.txt

COPY app ./app

# 内置 OFL 手写字体（V3 签名/印章）：
# 复制到 /opt/docforge/fonts 这个固定路径——它不会被宿主机对
# /usr/share/fonts/custom 的挂载遮蔽；fonts.py 把 /opt/docforge/fonts 列为高优先搜索目录。
# 运维挂载点 /usr/share/fonts/custom（simhei/msyh）仍作为兜底搜索路径保留。
COPY fonts /opt/docforge/fonts
RUN chmod -R a+r /opt/docforge/fonts || true

# 中文字体族名映射：**故意不在构建期写**。
#
# 模板里写的是「宋体」「黑体」，Linux 上没有这些族名，缺了会被 fontconfig 挑到
# Noto CJK 的日文面（直/骨/次字形不同），或者落到 DejaVu Sans（连汉字都没有）。
# 但这份配置不能烘进镜像：容器里的中文正版字体是**运行时挂载**进来的
# （/usr/share/fonts/custom），构建期看不到；构建期写死「宋体 → Noto Serif CJK SC」
# 会把运行时挂进来的正版宋体抢走 —— 不报错，只有对比 PDF 字形才发现。
#
# 改由 entrypoint 在字体可见之后按本机实际情况决定（fc-list 有就跳过），
# 再 setpriv 降到 appuser 跑服务。
COPY docker-entrypoint.sh /usr/local/bin/zb-entrypoint
RUN chmod +x /usr/local/bin/zb-entrypoint

# 非 root 运行；/tmp/docforge 是挂载点（named volume 首次挂载会继承镜像内该目录属主）
RUN useradd -m appuser \
    && mkdir -p /tmp/docforge \
    && chown -R appuser:appuser /srv/render-service /tmp/docforge

EXPOSE 8101

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD python -c "import urllib.request,sys; sys.exit(0 if urllib.request.urlopen('http://127.0.0.1:8101/healthz',timeout=3).status==200 else 1)"

# 入口以 root 起（只为了写一次字体配置），随后降权到 appuser。
ENTRYPOINT ["/usr/local/bin/zb-entrypoint"]
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8101"]
