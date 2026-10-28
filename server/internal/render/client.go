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
