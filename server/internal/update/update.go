// Package update 检查 GitHub 上是否有新版本。
//
// 设计上只做一件很小的事：**拿一个版本号**。不下载、不校验签名、不自动替换
// 二进制——那是包管理器与新部署的事，服务器上的自动替换是最容易把服务搞挂的
// 一类功能（下到一半、权限不对、正在跑的任务被中断）。
// 这里只负责「告诉你有新版本，点进去自己看」。
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"docforge/server/internal/version"
)

// Result 是一次检查的结果。
type Result struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	HasUpdate bool   `json:"has_update"`
	// Dev 表示当前是本地构建，版本号不可比较（前端据此不弹提示）。
	Dev bool `json:"dev"`
	// URL 是新版本的发布页；没有更新时为空。
	URL string `json:"url,omitempty"`
	// Name / Notes 是 release 的标题与正文（截断），用于弹窗里"一句话说明"。
	Name        string `json:"name,omitempty"`
	Notes       string `json:"notes,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
	// CheckedAt 是本次结果的产生时间；取自缓存时也是缓存产生的时间。
	CheckedAt string `json:"checked_at"`
	// Stale 表示这次没能刷新（网络不通 / 被限流），返回的是上一次的结果。
	//
	// 显式区分「查过了，没有新版本」与「没查成」：把后者当成前者，
	// 用户会以为更新检查在工作，其实一直是哑的。
	Stale bool `json:"stale,omitempty"`
}

// Checker 带缓存的检查器。并发安全。
type Checker struct {
	Repo    string
	Client  *http.Client
	TTL     time.Duration
	Current string
	// APIBase 是 GitHub API 根地址，默认 https://api.github.com。
	// 单独拎出来是为了测试能指向 httptest：把地址写死在 latestRelease 里，
	// 这个包的每一个用例都会真的去打外网——慢、需要联网、时不时红一次。
	APIBase string

	mu       sync.Mutex
	cached   Result
	cachedAt time.Time
	ok       bool // cached 是否是有效结果
}

// New 构造。client 为 nil 时用一个 4 秒超时的默认客户端。
func New(repo, current string) *Checker {
	if repo == "" {
		repo = "kinghtxg/zhuban"
	}
	return &Checker{
		Repo:    repo,
		Current: current,
		TTL:     6 * time.Hour,
		APIBase: "https://api.github.com",
		Client: &http.Client{
			// 超时必须短：这个接口挂在前端首屏之后，但用户在等它的时候
			// 什么都不该卡住。GitHub 慢或不通时宁可这轮没有结果。
			Timeout: 4 * time.Second,
		},
	}
}

// Check 返回更新检查结果。force=true 时忽略缓存。
func (c *Checker) Check(ctx context.Context, force bool) Result {
	c.mu.Lock()
	if !force && c.ok && time.Since(c.cachedAt) < c.TTL {
		r := c.cached
		c.mu.Unlock()
		return r
	}
	c.mu.Unlock()

	r := c.fetch(ctx)

	c.mu.Lock()
	if r.Stale {
		// 刷新失败：保留上一份结果，只把 stale 标出来。
		if c.ok {
			prev := c.cached
			prev.Stale = true
			prev.CheckedAt = r.CheckedAt
			c.mu.Unlock()
			return prev
		}
	} else {
		c.cached = r
		c.cachedAt = time.Now()
		c.ok = true
	}
	c.mu.Unlock()
	return r
}

// fetch 真正去 GitHub 拉一次。
func (c *Checker) fetch(ctx context.Context) Result {
	now := time.Now().UTC().Format(time.RFC3339)
	// Dev 从 Checker 自己的 Current 推，而不是读 version 包的全局值：
	// 两处取值一旦不一致，就会出现「Current=3.4.0 但被判成 dev 构建 →
	// 永远不提示更新」，而日志里一切正常。
	out := Result{Current: c.Current, Dev: c.isDev(), CheckedAt: now}

	rel, err := c.latestRelease(ctx)
	if err != nil {
		log.Printf("[update] 查询最新版本失败：%v", err)
		out.Stale = true
		return out
	}
	out.Latest = rel.Tag
	out.URL = rel.URL
	out.Name = rel.Name
	out.Notes = truncate(rel.Notes, 600)
	out.PublishedAt = rel.PublishedAt

	// 本地构建（Version == "dev"）不参与比较：没有意义，还会让人以为
	// "开发版落后于线上版"是个需要处理的问题。
	if !out.Dev && out.Latest != "" && version.Compare(out.Latest, out.Current) > 0 {
		out.HasUpdate = true
	}
	return out
}

type release struct {
	Tag         string
	Name        string
	Notes       string
	URL         string
	PublishedAt string
}

// latestRelease 先查 releases/latest，没有 release 就退回 tags。
//
// 为什么要有退路：这个项目的发布不一定每次都建 Release（打 tag 更省事）。
// 只查 releases 的话，tag 发布了却永远收不到更新提示，而且不报错。
func (c *Checker) latestRelease(ctx context.Context) (release, error) {
	var rel struct {
		TagName     string `json:"tag_name"`
		Name        string `json:"name"`
		Body        string `json:"body"`
		HTMLURL     string `json:"html_url"`
		PublishedAt string `json:"published_at"`
		Draft       bool   `json:"draft"`
		Prerelease  bool   `json:"prerelease"`
	}
	url := fmt.Sprintf("%s/repos/%s/releases/latest", c.apiBase(), c.Repo)
	status, err := c.getJSON(ctx, url, &rel)
	if err == nil && status == http.StatusOK {
		if rel.Draft || rel.Prerelease {
			// 预发布不提示：服务器上的版本升级应该是明确动作，不该被 rc 打断
			return release{}, nil
		}
		return release{
			Tag: rel.TagName, Name: firstNonEmpty(rel.Name, rel.TagName),
			Notes: rel.Body, URL: rel.HTMLURL, PublishedAt: rel.PublishedAt,
		}, nil
	}
	// 404 = 仓库还没有 Release，走 tags。
	if err != nil && status != http.StatusNotFound {
		return release{}, err
	}

	var tags []struct {
		Name string `json:"name"`
	}
	url = fmt.Sprintf("%s/repos/%s/tags?per_page=30", c.apiBase(), c.Repo)
	status, err = c.getJSON(ctx, url, &tags)
	if err != nil {
		return release{}, err
	}
	if status != http.StatusOK {
		return release{}, fmt.Errorf("tags 接口返回 %d", status)
	}
	best := ""
	for _, t := range tags {
		// 只接受形如 v3.4.0 / 3.4.0 的 tag；分支名之类的不参与比较。
		clean := strings.TrimPrefix(strings.TrimSpace(t.Name), "v")
		if !looksLikeSemver(clean) {
			continue
		}
		if best == "" || version.Compare(clean, best) > 0 {
			best = clean
		}
	}
	if best == "" {
		return release{}, nil
	}
	return release{
		Tag:  best,
		Name: "v" + best,
		URL:  fmt.Sprintf("https://github.com/%s/releases/tag/v%s", c.Repo, best),
	}, nil
}

// isDev 判断当前是否为本地构建（版本号不可比较）。
func (c *Checker) isDev() bool {
	v := strings.TrimSpace(c.Current)
	return v == "" || v == "dev"
}

// apiBase 返回 API 根地址，去掉尾斜杠并兜住零值。
func (c *Checker) apiBase() string {
	if strings.TrimSpace(c.APIBase) == "" {
		return "https://api.github.com"
	}
	return strings.TrimRight(c.APIBase, "/")
}

func looksLikeSemver(s string) bool {
	if s == "" || s[0] < '0' || s[0] > '9' {
		return false
	}
	dots := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r == '.':
			dots++
		case r == '-':
			return dots >= 1
		default:
			return false
		}
	}
	return dots >= 1
}

// getJSON 发请求并解 JSON。返回状态码，方便调用方区分 404 与其他错误。
func (c *Checker) getJSON(ctx context.Context, url string, dst interface{}) (int, error) {
	// 给 ctx 再套一层超时：调用方的 ctx 可能没有 deadline。
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	// GitHub 对没有 UA 的请求直接 403，且不会说明原因。
	req.Header.Set("User-Agent", "zhuban/"+version.Current().Version)
	resp, err := c.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	// 限流时 GitHub 返回 403 + 说明；这里只读一小段，避免把整页 HTML 读进来。
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return resp.StatusCode, fmt.Errorf("解析 GitHub 响应失败：%w", err)
	}
	return resp.StatusCode, nil
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
