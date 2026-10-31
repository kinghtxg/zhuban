#!/usr/bin/env bash
# 配置 Docker Hub 镜像加速并验证拉取
set -uo pipefail

mkdir -p /etc/docker
cat > /etc/docker/daemon.json <<'EOF'
{
  "registry-mirrors": [
    "https://docker.m.daocloud.io",
    "https://docker.1ms.run",
    "https://dockerproxy.net",
    "https://hub.rat.dev",
    "https://docker.xuanyuan.me",
    "https://docker.mirrors.ustc.edu.cn"
  ],
  "log-driver": "json-file",
  "log-opts": { "max-size": "20m", "max-file": "3" }
}
EOF

systemctl restart docker
sleep 4
echo "== docker info mirrors =="
docker info 2>/dev/null | grep -A8 "Registry Mirrors"

echo "== 尝试拉取 alpine:3.20 =="
for i in 1 2 3; do
  if docker pull alpine:3.20; then
    echo "PULL_OK on attempt $i"
    break
  else
    echo "attempt $i failed, retrying..."
    sleep 3
  fi
done

echo "== 尝试拉取 postgres:16-alpine =="
docker pull postgres:16-alpine && echo PG_OK
echo "== MIRROR_SETUP_DONE"
