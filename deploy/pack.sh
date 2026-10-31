#!/usr/bin/env bash
# 打一个用于上传到服务器的源码包。
#
# 为什么要单独有这个脚本：一次教训。手工敲 tar 时忘了排除 .env，
# 本地的开发配置（docforge-local-test 库密码、AUTH_DEV=true、OIDC 关闭）
# 随包覆盖了服务器上的生产 .env，容器起来就连不上库、AUTH_DEV=true
# 还会让服务信任 X-Dev-User 头，等于开了个免认证后门。
#
# 所以这里的排除项是**安全边界**，不是省空间的小技巧：
#   .env / deploy/.env.production  —— 环境配置归环境，绝不跨机搬运
#   verify/                        —— 本地验证产物，含会话 cookie
#   web/.auth                      —— Playwright 会话快照，里面有登录 cookie
#                                     打进构建上下文等于把它烤进镜像层
#   *.zip                          —— 手工备份，体积大且无意义
#
# 用法：
#   ./deploy/pack.sh              # 输出 /tmp/zb-deploy.tgz
#   ./deploy/pack.sh out.tar.gz   # 指定输出路径
set -euo pipefail

cd "$(dirname "$0")/.."

OUT="${1:-/tmp/zb-deploy.tgz}"
rm -f "$OUT"

tar \
  --exclude='./.git' \
  --exclude='./.env' \
  --exclude='./deploy/.env.production' \
  --exclude='./verify' \
  --exclude='./web/node_modules' \
  --exclude='./web/.auth' \
  --exclude='./web/test-results' \
  --exclude='./web/playwright-report' \
  --exclude='./web/dist/assets' \
  --exclude='./web/dist.*' \
  --exclude='./volumes' \
  --exclude='./data' \
  --exclude='./.tmp' \
  --exclude='./.tmp-*' \
  --exclude='./.tmp-run' \
  --exclude='./.tmp-shots' \
  --exclude='./_deploy.tgz' \
  --exclude='*.zip' \
  --exclude='__pycache__' \
  -czf "$OUT" .

# 打包后自检：包里出现环境文件就是打包脚本本身有问题，宁可失败也别传上去。
if tar tzf "$OUT" | grep -qE '^\./(\.env|deploy/\.env\.production)$'; then
  echo "打包失败：包内出现环境配置文件，请检查排除规则" >&2
  rm -f "$OUT"
  exit 1
fi

echo "已生成 $OUT ($(du -h "$OUT" | cut -f1))"
