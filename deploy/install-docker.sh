#!/usr/bin/env bash
# 在 CentOS Stream 9 上安装 Docker CE（与现有 podman 共存）
set -euo pipefail

if command -v docker >/dev/null 2>&1; then
  echo "== docker already installed: $(docker --version)"
  systemctl enable --now docker
  systemctl is-active docker
  exit 0
fi

echo "== 安装 dnf-plugins-core"
dnf install -y dnf-plugins-core

echo "== 添加 docker-ce 官方仓库"
dnf config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo
# 兼容 Stream 9：$releasever 为 9-stream，官方仓库目录是 9，做一次替换
sed -i 's#/centos/\$releasever/#/centos/9/#g' /etc/yum.repos.d/docker-ce.repo || true

echo "== 安装 docker-ce / compose 插件"
dnf install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

echo "== 启用并启动 docker"
systemctl enable --now docker
sleep 3
systemctl is-active docker

echo "== 版本信息"
docker --version
docker compose version

echo "== 运行 hello-world"
docker run --rm hello-world 2>&1 | head -5
echo "== DOCKER_INSTALL_DONE"
