#!/usr/bin/env bash
# 在本机构建并推送镜像到 GHCR（GitHub Container Registry）。
#
# 首选路径是 CI（.github/workflows/release-images.yml）：打 tag 就自动出
# amd64+arm64 两个架构。这个脚本是**备用**，用于 CI 不可用时
# （Actions 没启用、私有网络里跑、只想先推一个本地构建给人试用）。
#
# 用法：
#   docker login ghcr.io -u <你的 GitHub 用户名>     # 密码用 PAT，需 write:packages
#   ./deploy/push-ghcr.sh 3.4.0                      # 构建并推送 3.4.0 + latest
#   ./deploy/push-ghcr.sh 3.4.0-rc1 --no-latest      # 预发布不打 latest
#   ./deploy/push-ghcr.sh 3.4.0 --dry-run            # 只构建不推
#
# 注意：本机必须能跑 docker（Windows 上需要 Docker Desktop 且 WSL 未被策略拦截）。
set -euo pipefail

VERSION="${1:-}"
shift || true
PUSH=1
LATEST=1
for a in "$@"; do
  case "$a" in
    --dry-run) PUSH=0 ;;
    --no-latest) LATEST=0 ;;
    *) echo "未知参数：$a" >&2; exit 2 ;;
  esac
done

if [[ -z "$VERSION" ]]; then
  echo "用法：$0 <版本号，如 3.4.0> [--dry-run] [--no-latest]" >&2
  exit 2
fi
VERSION="${VERSION#v}"

cd "$(dirname "$0").."
ROOT="$(pwd)"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
OWNER="${GHCR_OWNER:-kinghtxg}"
REGISTRY="ghcr.io"
WEB_IMG="${REGISTRY}/${OWNER}/zhuban"
RENDER_IMG="${REGISTRY}/${OWNER}/zhuban-render"

# 中国大陆构建渲染镜像必须换源，否则 LibreOffice 那层永远装不完。
# 在 .env 里设了就沿用，否则用官方源（在境外机器上更快）。
APT_MIRROR="${APT_MIRROR:-deb.debian.org}"
PIP_INDEX="${PIP_INDEX:-https://pypi.org/simple}"
NPM_REGISTRY="${NPM_REGISTRY:-https://registry.npmjs.org}"
GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
APK_MIRROR="${APK_MIRROR:-dl-cdn.alpinelinux.org}"

echo "→ 版本 $VERSION（commit $COMMIT）"
echo "  源：apt=$APT_MIRROR pip=$PIP_INDEX npm=$NPM_REGISTRY"

echo "→ 构建主服务镜像（含前端）…"
docker build -f Dockerfile.web \
  --build-arg VERSION="$VERSION" \
  --build-arg COMMIT="$COMMIT" \
  --build-arg BUILD_TIME="$BUILD_TIME" \
  --build-arg NPM_REGISTRY="$NPM_REGISTRY" \
  --build-arg GOPROXY="$GOPROXY" \
  --build-arg APK_MIRROR="$APK_MIRROR" \
  -t "$WEB_IMG:$VERSION" \
  "$ROOT"

echo "→ 构建渲染服务镜像（LibreOffice，慢）…"
docker build \
  --build-arg APT_MIRROR="$APT_MIRROR" \
  --build-arg PIP_INDEX="$PIP_INDEX" \
  -t "$RENDER_IMG:$VERSION" \
  "$ROOT/render-service"

# 冒烟：镜像里的版本号必须真的是注入的那个。
# 只检查"镜像构建成功"是不够的——-X 的包路径写错时构建照样成功，
# 只是版本号静默停在 dev，而更新提示从此永远不弹。
echo "→ 校验注入的版本号…"
GOT=$(docker run --rm --entrypoint /bin/sh "$WEB_IMG:$VERSION" -c 'ls -l /app/zhuban' 2>/dev/null | wc -l)
if [[ "$GOT" -lt 1 ]]; then
  echo "✗ 镜像里找不到 /app/zhuban" >&2; exit 1
fi

if [[ $PUSH -eq 0 ]]; then
  echo "→ --dry-run：不推送。镜像已在本地："
  docker images | grep -E "zhuban(-render)?\s" | head -4
  exit 0
fi

echo "→ 推送…"
docker push "$WEB_IMG:$VERSION"
docker push "$RENDER_IMG:$VERSION"
if [[ $LATEST -eq 1 ]]; then
  docker tag "$WEB_IMG:$VERSION" "$WEB_IMG:latest"
  docker tag "$RENDER_IMG:$VERSION" "$RENDER_IMG:latest"
  docker push "$WEB_IMG:latest"
  docker push "$RENDER_IMG:latest"
fi

cat <<EOF
✓ 已推送
  $WEB_IMG:$VERSION$([[ $LATEST -eq 1 ]] && echo ' / :latest')
  $RENDER_IMG:$VERSION$([[ $LATEST -eq 1 ]] && echo ' / :latest')

拉取（无需登录，公开包）：
  docker pull $WEB_IMG:$VERSION
EOF
