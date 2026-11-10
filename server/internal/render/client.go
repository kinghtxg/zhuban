// Package render 是 Go 主服务到 Python 渲染微服务（契约 §3）的 HTTP 客户端。
// 用 Renderer 接口抽象，单测以 httptest mock 替换，不依赖真实渲染服务。
package render

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// Diagnostics 扫描/校验诊断项。
type Diagnostics struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Location string `json:"location"`
}

// ScanResult 对应契约 §3.1 /scan 200。
type ScanResult struct {
	Variables   []interface{} `json:"variables"`
	Tags        []string     `json:"tags"`
	SampleJSON  interface{}  `json:"sample_json"`
	JSONSchema  interface{}  `json:"json_schema"`
	Diagnostics []Diagnostics `json:"diagnostics"`
	Repaired    bool         `json:"repaired"`
	// FontAudit 是模板用字体检（3.4.13）。缺字体这件事**一直是不报错的**：
	// 接口 200、正文全对、PDF 能打开，只有字形被悄悄换掉。所以要在录入那一刻
	// 把话说清楚。它是提醒，不是闸门——体检自身失败（Ok=false）也不阻断上传。
	FontAudit *FontAudit `json:"font_audit,omitempty"`
}

// FontAuditEntry 是单个字体族的体检结论。
type FontAuditEntry struct {
	Family string `json:"family"`
	// Status ∈ exact（真装了）/ fallback（会被换成本机另一种中文字体，字形变）
	// / missing（落到不含汉字的字体，汉字只能逐字回退）/ latin（纯拉丁，被
	// Liberation 之类公制兼容字体替换属正常，不进告警）。
	Status string `json:"status"`
	// Resolved 实际会用的字体（fc-match 结果）。
	Resolved string `json:"resolved"`
	// Parts 这个族名出现在哪些部件：正文 / 样式 / 主题 / 编号 / 页眉…
	Parts []string `json:"parts"`
	// CleanName 剥掉括号注解后的族名。WPS 会把主题槽位的**界面显示名**
	// 写进 rFonts（如 "Calibri (西文正文)"），判断该按括号前的名字做。
	CleanName string `json:"clean_name,omitempty"`
	// Note 族名可疑时的一句说明，为空表示没意见。
	Note string `json:"note,omitempty"`
}

// FontAuditSlot 某一支字体在某个文字槽位上的归属（段落级归因用）。
type FontAuditSlot struct {
	Family   string `json:"family"`
	Status   string `json:"status"`
	Resolved string `json:"resolved"`
}

// FontAuditRoleFont 是某一个文字角色下的一支字体。
type FontAuditRoleFont struct {
	Family   string   `json:"family"`
	Status   string   `json:"status"`
	Resolved string   `json:"resolved"`
	// Segments 有多少段实际用到这支字体（按段计，不按族名出现在几个部件计）。
	Segments int      `json:"segments"`
	Parts    []string `json:"parts"`
}

// FontAuditRole 一个文字角色（中文 / 西文）用了哪些字体，按段数从多到少。
type FontAuditRole struct {
	Label string              `json:"label"`
	Fonts []FontAuditRoleFont `json:"fonts"`
}

// FontAuditParagraph 是「这一段里汉字用什么、西文用什么」的逐段归因。
//
// 为什么要到段：中英混排的段落里汉字走 eastAsia、西文走 ascii/hAnsi，
// 本来就是两支字体。只给一张不分角色的族名清单，用户看得见
// 「仿宋_GB2312」和「Times New Roman」两行，却答不上「这段汉字是哪支」。
type FontAuditParagraph struct {
	Part  string         `json:"part"`
	Index int            `json:"index"`
	Style string         `json:"style,omitempty"`
	Text  string         `json:"text"`
	CJK   *FontAuditSlot `json:"cjk"`
	Latin *FontAuditSlot `json:"latin"`
}

// FontAuditSuspect 是「看着不像真字体」的族名。
//
// WPS 写进去的 "Calibri (西文正文)" 这类名字不是缺字体，是脏数据：
// 它不该进 Missing/Severe（会导致假报，三次假报之后真报警就没人看了），
// 但也不该完全不说——用户按这个名字去搜字体是搜不到的。
type FontAuditSuspect struct {
	Family string `json:"family"`
	Reason string `json:"reason"`
}

// FontAudit 是一份 docx 的字体体检报告（render-service /fontaudit）。
type FontAudit struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Checked 参与判定的族名个数（已剔除主题里的泰文/日文等噪音槽位）。
	Checked int              `json:"checked"`
	Missing []string         `json:"missing"` // fallback + missing 的族名
	Severe  []string         `json:"severe"`  // 仅 missing（最坏的那一档）
	Fonts   []FontAuditEntry `json:"fonts"`
	// Roles 按文字角色分开统计：eastAsia=中文、latin=西文、cs=复杂文种。
	Roles map[string]FontAuditRole `json:"roles,omitempty"`
	// Paragraphs 前若干段的逐段归属（渲染服务限量，默认 40 段）。
	Paragraphs []FontAuditParagraph `json:"paragraphs,omitempty"`
	// Suspects 脏族名清单，单独列，不进 Missing/Severe。
	Suspects []FontAuditSuspect `json:"suspects,omitempty"`
	// RunsError 逐段归因自身失败的原因；为空表示归因成功。
	// 归因失败不影响上面的 face 级结论，所以它是注释不是错误。
	RunsError string `json:"runs_error,omitempty"`
}

// FieldError 校验错误。
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidateResult 对应 §3.3。
type ValidateResult struct {
	Valid    bool         `json:"valid"`
	Errors   []FieldError `json:"errors"`
	Warnings []FieldError `json:"warnings"`
}

// PreviewPage 对应 §3.4 pages[]。
type PreviewPage struct {
	Page   int    `json:"page"`
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// PreviewResult 对应 §3.4。
type PreviewResult struct {
	PreviewID    string        `json:"preview_id"`
	PageCount    int           `json:"page_count"`
	ReturnedPages int          `json:"returned_pages"`
	Pages        []PreviewPage `json:"pages"`
}

// Health 对应 §3.5。
type Health struct {
	Status    string `json:"status"`
	LibreOffice bool `json:"libreoffice"`
	Poppler   bool   `json:"poppler"`
	Version   string `json:"version"`
}

// ParseResult 对应 V2 render-service POST /parse：docx → 结构化文档模型。
// Blocks 保留原始 JSON 数组（透传给 /apply，不做结构化解析，避免丢字段）。
type ParseResult struct {
	Blocks   json.RawMessage `json:"blocks"`
	Warnings []string        `json:"warnings"`
	// Page 是纸张尺寸与页边距（mm）。编辑器画布靠它还原本来的版心，
	// 不还原的话画布只是一个白盒子，用户看到的排版和生成结果对不上。
	Page json.RawMessage `json:"page,omitempty"`
}

// Font 一个可选签名字体（V3 §6.1 /fonts）。
type Font struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// InstalledFont 是 fontconfig 已识别的字体（含运维导入的）。
type InstalledFont struct {
	File     string `json:"file"`
	Family   string `json:"family"`
	Style    string `json:"style"`
	Path     string `json:"path"`
	Imported bool   `json:"imported"`
}

// ReloadResult 是字体缓存刷新结果。
type ReloadResult struct {
	OK        bool     `json:"ok"`
	ElapsedMS int      `json:"elapsed_ms"`
	ImportDir string   `json:"import_dir"`
	Errors    []string `json:"errors"`
}

// Error 渲染服务返回的错误（透传契约 §2 信封）。
type Error struct {
	StatusCode int
	Code       string
	Message    string
	Details    []FieldError
}

func (e *Error) Error() string { return fmt.Sprintf("render %d %s: %s", e.StatusCode, e.Code, e.Message) }

// Renderer 抽象渲染服务调用。
type Renderer interface {
	Scan(ctx context.Context, docx []byte) (*ScanResult, error)
	Render(ctx context.Context, docx []byte, dataJSON []byte, format string) ([]byte, string, error) // 返回二进制与 content-type
	Validate(ctx context.Context, docx []byte, dataJSON []byte) (*ValidateResult, error)
	Preview(ctx context.Context, docx []byte, dataJSON []byte, maxPages int) (*PreviewResult, error)
	PreviewPage(ctx context.Context, pv string, n int) ([]byte, error)
	Health(ctx context.Context) (*Health, error)
	// V2：docx ↔ 结构化文档模型
	ParseModel(ctx context.Context, docx []byte) (*ParseResult, error)
	ApplyModel(ctx context.Context, docx []byte, modelJSON []byte) ([]byte, error)
	// V3：签名/印章图生成（multipart 字段 → image/png）
	RenderSignature(ctx context.Context, fields map[string]string) ([]byte, error)
	RenderSeal(ctx context.Context, fields map[string]string) ([]byte, error)
	Fonts(ctx context.Context) ([]Font, error)
	// ReloadFonts 让渲染服务刷新 fontconfig 缓存（导入字体后调用）。
	ReloadFonts(ctx context.Context) (*ReloadResult, error)
	// InstalledFonts 列出 fontconfig 已识别的字体。
	InstalledFonts(ctx context.Context) ([]InstalledFont, error)
	// FontAudit 单独体检一份 docx 的用字情况（不建模板也能查）。
	// 供「按需重新体检」用：管理员导入了新字体之后，老模板的结论需要能刷新。
	FontAudit(ctx context.Context, docx []byte) (*FontAudit, error)
	// Align 把用户自由写的一整篇内容对齐到模板的各处槽位，返回建议（不落笔）。
	//
	// 刻意透传原始 JSON 而不在 Go 侧建结构体：对齐结果里带 paragraphs/slots
	// 两份完整镜像（前端要用来画"这一段落到了哪儿"），字段会随算法演进——
	// 在 Go 里再翻译一遍只会多一处要同步维护的假真相。
	Align(ctx context.Context, docx []byte, content string, mime string) (json.RawMessage, error)
}

// Client 是 Renderer 的真实 HTTP 实现。
type Client struct {
	Base string
	HC   *http.Client

	ScanTO    time.Duration
	RenderTO  time.Duration
	PreviewTO time.Duration
}

// New 构造 HTTP 客户端。
func New(base string, scanTO, renderTO, previewTO time.Duration) *Client {
	return &Client{
		Base: strings.TrimRight(base, "/"),
		HC:   &http.Client{Timeout: renderTO + 10*time.Second},
		ScanTO: scanTO, RenderTO: renderTO, PreviewTO: previewTO,
	}
}

func (c *Client) do(ctx context.Context, timeout time.Duration, fn func(req *http.Request)) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", c.Base, nil)
	if err != nil {
		return nil, err
	}
	fn(req)
	return c.HC.Do(req)
}

// postMultipart 构造 multipart 请求。fields 为额外文本字段，文件字段名 file。
// 注意：调用方负责 context 超时与取消，且必须在读完/关闭 resp.Body 后再 cancel，
// 否则 cancel 会中断响应体读取（曾导致"context canceled"）。
func (c *Client) postMultipart(ctx context.Context, path string,
	fileName string, file []byte, fields map[string]string) (*http.Response, error) {

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", fileName)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(file); err != nil {
		return nil, err
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	_ = w.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", c.Base+path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	return c.HC.Do(req)
}

// decodeErr 解析 render-service 错误信封。
func decodeErr(resp *http.Response) *Error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	env := struct {
		Error struct {
			Code    string        `json:"code"`
			Message string        `json:"message"`
			Details []FieldError  `json:"details"`
		} `json:"error"`
	}{}
	_ = json.Unmarshal(body, &env)
	if env.Error.Code == "" {
		env.Error.Code = "internal_error"
		env.Error.Message = fmt.Sprintf("渲染服务返回异常（HTTP %d）", resp.StatusCode)
	}
	return &Error{StatusCode: resp.StatusCode, Code: env.Error.Code, Message: env.Error.Message, Details: env.Error.Details}
}

// Scan 调 /scan。
func (c *Client) Scan(ctx context.Context, docx []byte) (*ScanResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.ScanTO)
	defer cancel()
	resp, err := c.postMultipart(ctx, "/scan", "template.docx", docx, map[string]string{"repair": "true"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeErr(resp)
	}
	var out ScanResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ParseModel 调 /parse：docx → 结构化文档模型（blocks 原样返回）。
func (c *Client) ParseModel(ctx context.Context, docx []byte) (*ParseResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.ScanTO)
	defer cancel()
	resp, err := c.postMultipart(ctx, "/parse", "template.docx", docx, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeErr(resp)
	}
	var out ParseResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ApplyModel 调 /apply：以 file（docx）+ model（结构化模型 JSON 字符串）原地回写，
// 返回回写后的 docx 二进制。
func (c *Client) ApplyModel(ctx context.Context, docx []byte, modelJSON []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.ScanTO)
	defer cancel()
	resp, err := c.postMultipart(ctx, "/apply", "template.docx", docx,
		map[string]string{"model": string(modelJSON)})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeErr(resp)
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Render 调 /render，返回二进制与 content-type。
func (c *Client) Render(ctx context.Context, docx []byte, dataJSON []byte, format string) ([]byte, string, error) {
	if format == "" || format == "both" {
		format = "docx"
	}
	ctx, cancel := context.WithTimeout(ctx, c.RenderTO)
	defer cancel()
	resp, err := c.postMultipart(ctx, "/render", "template.docx", docx,
		map[string]string{"data": string(dataJSON), "format": format})
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", decodeErr(resp)
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if err != nil {
		return nil, "", err
	}
	return out, resp.Header.Get("Content-Type"), nil
}

// Validate 调 /validate。
func (c *Client) Validate(ctx context.Context, docx []byte, dataJSON []byte) (*ValidateResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.ScanTO)
	defer cancel()
	resp, err := c.postMultipart(ctx, "/validate", "template.docx", docx,
		map[string]string{"data": string(dataJSON)})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeErr(resp)
	}
	var out ValidateResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Preview 调 /preview。
func (c *Client) Preview(ctx context.Context, docx []byte, dataJSON []byte, maxPages int) (*PreviewResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.PreviewTO)
	defer cancel()
	resp, err := c.postMultipart(ctx, "/preview", "template.docx", docx,
		map[string]string{"data": string(dataJSON), "max_pages": fmt.Sprintf("%d", maxPages)})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeErr(resp)
	}
	var out PreviewResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PreviewPage 代理回渲染服务的单页 PNG。
func (c *Client) PreviewPage(ctx context.Context, pv string, n int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.PreviewTO)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/preview/%s/page/%d", c.Base, pv, n), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("preview page http %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
}

// Health 调 /healthz。
func (c *Client) Health(ctx context.Context) (*Health, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", c.Base+"/healthz", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("health http %d", resp.StatusCode)
	}
	var h Health
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return nil, err
	}
	return &h, nil
}

// postFormOnly 纯文本 multipart（无文件字段），用于签名/印章图像生成。
func (c *Client) postFormOnly(ctx context.Context, path string, fields map[string]string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	_ = w.Close()
	req, err := http.NewRequestWithContext(ctx, "POST", c.Base+path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := c.HC.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeErr(resp)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
}

// RenderSignature 调 POST /signature/render，返回透明底 PNG。
func (c *Client) RenderSignature(ctx context.Context, fields map[string]string) ([]byte, error) {
	return c.postFormOnly(ctx, "/signature/render", fields, c.RenderTO)
}

// RenderSeal 调 POST /seal/render，返回红色印章 PNG。
func (c *Client) RenderSeal(ctx context.Context, fields map[string]string) ([]byte, error) {
	return c.postFormOnly(ctx, "/seal/render", fields, c.RenderTO)
}

// Fonts 调 GET /fonts。
func (c *Client) Fonts(ctx context.Context) ([]Font, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", c.Base+"/fonts", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeErr(resp)
	}
	var out struct {
		Fonts []Font `json:"fonts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Fonts, nil
}

// ReloadFonts POST /fonts/reload
func (c *Client) ReloadFonts(ctx context.Context) (*ReloadResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second) // fc-cache 在字体多时可达十几秒
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", c.Base+"/fonts/reload", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeErr(resp)
	}
	var out ReloadResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// InstalledFonts GET /fonts/installed
func (c *Client) InstalledFonts(ctx context.Context) ([]InstalledFont, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", c.Base+"/fonts/installed", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeErr(resp)
	}
	var out struct {
		Fonts []InstalledFont `json:"fonts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Fonts, nil
}

// FontAudit 调 POST /fontaudit：docx → 字体体检报告。
func (c *Client) FontAudit(ctx context.Context, docx []byte) (*FontAudit, error) {
	ctx, cancel := context.WithTimeout(ctx, c.ScanTO)
	defer cancel()
	resp, err := c.postMultipart(ctx, "/fontaudit", "template.docx", docx, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeErr(resp)
	}
	var out FontAudit
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Align 调 POST /align：docx + 自由文本 → 槽位对齐建议（原始 JSON 透传）。
//
// 上限放到 32MB：用户在富文本里贴的截图是 data URI 内联的，一篇带五六张
// 截图的报告轻松就是十几 MB，而返回体里还要把这些图再镜像一遍。
func (c *Client) Align(ctx context.Context, docx []byte, content, mime string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.ScanTO)
	defer cancel()
	resp, err := c.postMultipart(ctx, "/align", "template.docx", docx,
		map[string]string{"content": content, "mime": mime})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeErr(resp)
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if !json.Valid(out) {
		return nil, fmt.Errorf("align: 渲染服务返回的不是 JSON（%d 字节）", len(out))
	}
	return json.RawMessage(out), nil
}
