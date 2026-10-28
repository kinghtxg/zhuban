#!/usr/bin/env bash
# 把当前 HEAD 发布到公开仓库 github.com/kinghtxg/zhuban。
#
# ⚠️ 为什么不能直接 `git push github main`：
# 开发仓库的历史里有与代码无关的凭据（旧提交里出现过管理员口令）。整库推送
# 等于把那些东西一起公开，而且 GitHub 上的提交会被镜像/归档，删了也未必消失。
#
# 所以这里走「干净快照」：从当前 HEAD 导出一份没有 .git 的树，在一个全新仓库里
# 做一次提交，再 force 推到公开仓。公开仓的历史永远是「一个（或几个）发布快照」，
# 与开发历史无关。
#
# 代价：公开仓看不到开发过程。这是有意的取舍——过程可以另外写 CHANGELOG，
# 凭据泄露不可撤销。
#
# 用法：
#   ./deploy/publish-public.sh            # 发布当前 HEAD
#   ./deploy/publish-public.sh --dry-run  # 只看会推什么，不推
set -euo pipefail

REPO_URL="${PUBLIC_REPO_URL:-https://github.com/kinghtxg/zhuban.git}"
DRY=0
[[ "${1:-}" == "--dry-run" ]] && DRY=1

cd "$(dirname "$0")/.."
ROOT="$(pwd)"

# ---- 前置检查 ---------------------------------------------------------------
if ! git rev-parse --git-dir >/dev/null 2>&1; then
  echo "✗ 不在 git 仓库里" >&2; exit 1
fi
if [[ -n "$(git status --porcelain)" ]]; then
  echo "✗ 工作区有未提交改动。先提交，否则发布出去的和你看到的不是同一份。" >&2
  git status --short | head -10 >&2
  exit 1
fi

# 硬检查：被跟踪的文件里不能有真实 .env（.env.example 是模板，允许）
if git ls-files | grep -qE '(^|/)\.env$|(^|/)\.env\.production$'; then
  echo "✗ 仓库跟踪了 .env。绝不能发布。" >&2; exit 1
fi

# 临时目录用 POSIX 路径：Windows 上的 TMPDIR 是 C://... 这种形式，
# 交给 tar 会被反斜杠搞成"C\:..."，报 Cannot open。
BASE=/tmp
[[ "${TMPDIR:-}" == /* ]] && BASE="$TMPDIR"
TMP="$BASE/zbpub.$$"
mkdir -p "$TMP"
trap 'rm -rf "$TMP"' EXIT

echo "→ 导出 HEAD（$(git rev-parse --short HEAD)）到临时目录…"
git archive --format=tar HEAD | tar -x -C "$TMP"

# ---- 排除内部文档 -----------------------------------------------------------
# 这些是开发期的过程记录（验收报告、修复记录、部署日记、PRD 草稿……），
# 里面有真实的服务器地址、内网拓扑、事故经过与当时的凭据处置细节。
# 它们对使用者没有价值，对攻击者有价值，所以不进公开仓。
# 用「删除导出的副本」而不是把它们移出 git：内部仓库仍然留着，追溯得到。
INTERNAL_DOCS=(
  'docs/修复记录-*.md'
  'docs/验收报告-*.md'
  'docs/验收记录-*.md'
  'docs/部署记录-*.md'
  'docs/产品与实现文档-*.md'
  'docs/本轮能力补充-*.md'
  'docs/诊断-*.md'
  'docs/Git仓库使用说明.md'
)
rm -rf "$TMP/docs/prd"
for pat in "${INTERNAL_DOCS[@]}"; do
  # 逐个 glob 展开再删；没匹配到也不报错（新增/改名不该让发布失败）
  for f in $TMP/$pat; do [[ -e "$f" ]] && rm -f "$f"; done
done
echo "  已排除内部文档（验收/修复/部署记录、PRD 草稿共 ${#INTERNAL_DOCS[@]} 类）"

# ---- 泄露扫描（硬闸门）------------------------------------------------------
# 上面那些是「已知的内部文件」，这一层防的是「我忘了某处还写着自己的东西」。
# 命中即中止，不给"先推上去再改"的机会：GitHub 上的提交会被镜像，
# 删掉也不能保证消失。
#
# 允许保留的：仓库地址与官网页（github.com/kinghtxg/... 与 zb.kinghtxg.com），
# 它们是产品对外的正式入口。所以这里的 kinght 规则写成「不跟 xg」。
LEAK_PATTERNS=(
  '安璞'
  '极客星空'
  # 内部项目名会以 anpu.info（git email）、anpu-notes（项目名）等形式出现，
  # 只写 'anpu\.' 会漏掉连字符那种 —— 2026-10-29 就是这么漏了 login.css 的注释。
  'anpu[-_.]'
  'kinght([^x]|$)'
  '43\.138\.243\.34'
  '192\.168\.31\.'
  'sso\.kinghtxg\.com'
)
leak_hit=0
for pat in "${LEAK_PATTERNS[@]}"; do
  # 扫描范围排除这个脚本自己：它的内容就是上面那张模式表，必然自匹配。
  # 代价是「有人往闸门脚本里藏泄露」不会被它自己发现 —— 但闸门脚本每次
  # 发布都会被人读一遍，比放过整批模板文件划算。
  hits=$(grep -rnIE "$pat" "$TMP" \
    --exclude-dir=.git --exclude-dir=node_modules \
    --exclude='publish-public.sh' \
    --exclude='*.docx' --exclude='*.xlsx' --exclude='*.png' --exclude='*.jpg' \
    --exclude='*.woff*' --exclude='*.ttf' --exclude='*.otf' 2>/dev/null | head -8 || true)
  if [[ -n "$hits" ]]; then
    echo "✗ 命中泄露模式 /$pat/：" >&2
    echo "$hits" >&2
    leak_hit=1
  fi
done
if [[ $leak_hit -eq 1 ]]; then
  echo "" >&2
  echo "发布已中止。上面这些位置含个人信息/内网地址，改掉再发。" >&2
  exit 1
fi
echo "  泄露扫描通过（公司名/邮箱/内网 IP/SSO 域名均无残留）"

# ---- Office 文件元数据剥离（2026-10-29 补）---------------------------------
# 上面那层 grep 按设计跳过了 *.docx / *.xlsx —— 二进制没法按行扫。但这恰恰是
# 最容易漏的地方：Office 文件在 docProps/ 里存着创建者、最后修改人、以及 WPS
# 的设备指纹与账号 ID（custom.xml 里一段 base64，解出来是 {"hdid":...,"userId":...}）。
# 实测审计模板里带着使用者的网名和 WPS 账号，一路发到了公开仓。
#
# 所以这里再兜一层：在导出的副本上剥离元数据（不动源文件），剥离后复查，
# 有残留就中止。源文件由 samples/tools/strip-docx-metadata.py 负责清理。
if [[ -f "$TMP/samples/tools/strip-docx-metadata.py" ]]; then
  OFFICE_N=$(find "$TMP" -type f \( -name '*.docx' -o -name '*.xlsx' -o -name '*.pptx' \) | wc -l | tr -d ' ')
  if [[ "$OFFICE_N" -gt 0 ]]; then
    # 必须在 $TMP 里跑：脚本无参数时按 samples/**/*.docx 相对路径找文件，
    # 在项目根目录跑会去改源文件（那是另一件事，由人工显式执行）。
    OUT=$(cd "$TMP" && python3 samples/tools/strip-docx-metadata.py 2>&1) || {
      echo "✗ Office 元数据剥离失败：" >&2
      echo "$OUT" >&2
      exit 1
    }
    if echo "$OUT" | grep -q "含 "; then
      echo "  已剥离 $OFFICE_N 个 Office 文件的 docProps 元数据（创建者/修改人/设备与账号 ID）"
    fi
    # 复查：剥离后不应再有可疑标记
    if (cd "$TMP" && python3 samples/tools/strip-docx-metadata.py --check >/dev/null 2>&1); then
      echo "  Office 元数据复查通过"
    else
      echo "✗ Office 文件剥离后仍有残留，发布中止。" >&2
      (cd "$TMP" && python3 samples/tools/strip-docx-metadata.py --check >&2) || true
      exit 1
    fi
  fi
else
  echo "✗ 缺少 samples/tools/strip-docx-metadata.py，无法清理 Office 元数据，发布中止。" >&2
  exit 1
fi

FILES=$(find "$TMP" -type f | wc -l | tr -d ' ')
SIZE=$(du -sh "$TMP" | cut -f1)
echo "  文件 $FILES 个，共 $SIZE"

# 单文件上限：GitHub 超过 100MB 直接拒收，50MB 会告警。提前说清楚，
# 别等 push 到一半才失败。
BIG=$(find "$TMP" -type f -size +50M | head -5)
if [[ -n "$BIG" ]]; then
  echo "✗ 有超过 50MB 的文件，GitHub 会告警或拒收：" >&2
  echo "$BIG" >&2
  exit 1
fi

cd "$TMP"
git init -q -b main
git config user.name "$(git -C "$ROOT" config user.name || echo zhuban)"
git config user.email "$(git -C "$ROOT" config user.email || echo zhuban@example.com)"
git add -A
# 发布提交信息：默认取 HEAD 的 subject，但要剥掉 conventional-commit 前缀，
# 否则会出现 `release: docs: 补上……` 这种叠词。要用别的话说时给 PUBLIC_COMMIT_MSG。
SUMMARY="${PUBLIC_COMMIT_MSG:-$(git -C "$ROOT" log -1 --pretty=%s \
  | sed -E 's/^[a-z]+(\([^)]*\))?!?:[[:space:]]*//')}"
git commit -q -m "release: ${SUMMARY}

源码快照，导出时间 $(date '+%Y-%m-%d %H:%M')，不含开发历史。
对应内部提交 $(git -C "$ROOT" rev-parse --short HEAD)。"

if [[ $DRY -eq 1 ]]; then
  echo "→ --dry-run：将要推送的内容如下（未实际推送）"
  git log --stat --oneline | head -30
  exit 0
fi

echo "→ 推送到 $REPO_URL"
# force 是有意的：公开仓只保存发布快照，不做增量合并。
git push --force "$REPO_URL" main:main
echo "✓ 已发布。"
