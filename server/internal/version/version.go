// Package version 提供构建期写入的版本信息。
//
// 三个变量用 -ldflags 在打包时注入，源码里保持可编译的默认值：
//
//	go build -ldflags "\
//	  -X docforge/server/internal/version.Version=3.4.0 \
//	  -X docforge/server/internal/version.Commit=$(git rev-parse --short HEAD) \
//	  -X docforge/server/internal/version.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
//	  -o zhuban ./server
//
// 为什么不读 VERSION 文件：二进制会被单独拷来拷去，文件不一定跟着走；
// 编进二进制里的值永远不会和运行中的代码对不上。
package version

import "strings"

// 由 -ldflags -X 覆盖。
var (
	// Version 语义化版本号，形如 3.4.0（不带 v 前缀，方便比较）。
	Version = "dev"
	// Commit 构建所用的提交（短 hash）。
	Commit = "unknown"
	// BuildTime 构建时间（RFC3339，UTC）。
	BuildTime = "unknown"
)

// Info 是对外暴露的版本信息。
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	// Dev 表示这是未经打包的本地构建（Version == "dev"）。
	//
	// 前端据此隐藏更新提示：本地构建的版本号没有可比性，
	// 弹「有新版本」只会让开发者困惑。
	Dev bool `json:"dev"`
}

// Current 返回当前版本信息。
func Current() Info {
	v := strings.TrimSpace(Version)
	if v == "" {
		v = "dev"
	}
	return Info{
		Version:   v,
		Commit:    Commit,
		BuildTime: BuildTime,
		Dev:       v == "dev",
	}
}

// Compare 比较两个语义化版本，返回 -1 / 0 / 1。
//
// 只处理 release 版本（MAJOR.MINOR.PATCH）和可选的预发布后缀：
// 前缀 v 会被忽略，缺省的段按 0 处理（"3.4" == "3.4.0"）。
// 预发布（3.4.0-rc1）小于同号正式版，这是 semver 的规定——
// 少了这一条，「升级到 rc」会被误判成升级。
func Compare(a, b string) int {
	pa, preA := split(a)
	pb, preB := split(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] > pb[i] {
				return 1
			}
			return -1
		}
	}
	// 数字部分相同，再看预发布后缀：没有后缀的更大。
	switch {
	case preA == "" && preB == "":
		return 0
	case preA == "":
		return 1
	case preB == "":
		return -1
	case preA == preB:
		return 0
	case preA > preB:
		return 1
	default:
		return -1
	}
}

// split 把 "v3.4.1-rc2" 拆成 [3,4,1] 与 "rc2"。
func split(v string) ([3]int, string) {
	var out [3]int
	s := strings.TrimSpace(strings.ToLower(v))
	s = strings.TrimPrefix(s, "v")
	pre := ""
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
	}
	// 允许 "3.4.0+build5" 这种构建元数据，直接截掉（它不参与比较）
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	for i, part := range strings.Split(s, ".") {
		if i > 2 {
			break
		}
		n := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		out[i] = n
	}
	return out, pre
}
