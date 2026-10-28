// Package callback 负责任务终结后向调用方 callback_url 推送结果（契约 §5、PRD §11）。
// 含 SSRF 防护、10s 超时、禁止重定向、1s/4s/16s 退避 3 次，异步不阻塞 worker。
package callback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"docforge/server/internal/config"
	"docforge/server/internal/idgen"
	"docforge/server/internal/models"
	"docforge/server/internal/netguard"
	"docforge/server/internal/sign"

	"gorm.io/gorm"
)

func storeRandID() string { return idgen.WithPrefix("cbl_", 8) }

// Sender 发送回调。
type Sender struct {
	DB     *gorm.DB
	Cfg    *config.Config
	Signer *sign.Signer
	Guard  *netguard.Checker
}

// New 构造。
func New(db *gorm.DB, cfg *config.Config, sg *sign.Signer, guard *netguard.Checker) *Sender {
	return &Sender{DB: db, Cfg: cfg, Signer: sg, Guard: guard}
}

// clientFor 造一个「只连得上 t 里那几个 IP」的 HTTP 客户端。
//
// 每个目标单独造 Transport 而不是共用一个：Transport 的连接池按地址复用，
// 把不同回调目标的连接混在一个池里既不划算也说不清（重试时目标 host 可能
// 已经变了）。回调量级是「每个任务一两次」，这点开销可以忽略。
func (s *Sender) clientFor(t *netguard.Target) *http.Client {
	tr := &http.Transport{
		// 关键：拨号目标由 Resolve 固定，这里不再解析域名。
		DialContext: t.DialContext,
		// 不读 HTTP(S)_PROXY：回调地址是调用方给的，走环境代理等于把 SSRF 防护
		// 交给代理去执行，而代理并不认这份禁止名单。
		Proxy:               nil,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: tr,
		// 禁止跟随重定向：3xx 后面的地址没有经过校验，跟过去等于把 SSRF 防线
		// 让给对端决定。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type downloadItem struct {
	Filename string `json:"filename"`
	SignedURL string `json:"signed_url"`
}
type errorItem struct {
	RecordIndex int    `json:"record_index"`
	Error      string `json:"error"`
	Path       string `json:"path,omitempty"`
}
type payload struct {
	JobID       string        `json:"job_id"`
	ExternalRef string        `json:"external_ref"`
	Status      string        `json:"status"`
	Total       int           `json:"total"`
	Succeeded   int           `json:"succeeded"`
	Failed      int           `json:"failed"`
	Downloads   []downloadItem `json:"downloads"`
	Errors      []errorItem   `json:"errors"`
}

// SendAsync 异步发送回调，按退避重试，落库 callback_attempts/status。
func (s *Sender) SendAsync(job *models.Job) {
	if job.CallbackURL == "" {
		return
	}
	go s.sendWithRetry(job)
}

func (s *Sender) sendWithRetry(job *models.Job) {
	delays := []time.Duration{1 * time.Second, 4 * time.Second, 16 * time.Second}
	s.DB.Model(job).Update("callback_status", "pending")
	var lastErr error
	attempts := 0
	for attempt := 1; attempt <= 3; attempt++ {
		err := s.once(job)
		s.DB.Model(job).Update("callback_attempts", attempt)
		attempts = attempt
		if err == nil {
			s.DB.Model(job).Update("callback_status", "succeeded")
			s.writeLog(job, "succeeded", attempts, "")
			return
		}
		lastErr = err
		if attempt < 3 {
			time.Sleep(delays[attempt-1])
		}
	}
	s.DB.Model(job).Update("callback_status", "failed")
	s.writeLog(job, "failed", attempts, errSummary(lastErr))
}

// writeLog 记录一次回调生命周期到 callback_logs。
func (s *Sender) writeLog(job *models.Job, status string, attempts int, errMsg string) {
	summary := errMsg
	if len(summary) > 480 {
		summary = summary[:480]
	}
	rec := models.CallbackLog{
		ID: storeRandID(), JobID: job.ID, CallbackURL: job.CallbackURL,
		Status: status, Attempts: attempts, ErrorSummary: summary,
	}
	if err := s.DB.Create(&rec).Error; err != nil {
		// 日志写失败不影响主流程。
	}
}

func errSummary(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Sender) once(job *models.Job) error {
	// 校验与拨号必须共用同一次 DNS 结果：分开解析的话，攻击者的域名可以在
	// 校验时返回公网地址、在拨号时返回 169.254.169.254（DNS rebinding）。
	target, err := s.Guard.Resolve(job.CallbackURL)
	if err != nil {
		return err
	}
	p, err := s.buildPayload(job)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(p)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.CallbackURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.clientFor(target).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("callback http %d", resp.StatusCode)
	}
	return nil
}

func (s *Sender) buildPayload(job *models.Job) (*payload, error) {
	var docs []models.Document
	s.DB.Where("job_id = ?", job.ID).Order("record_index").Find(&docs)
	p := &payload{
		JobID: job.ID, ExternalRef: job.ExternalRef, Status: job.Status,
		Total: job.Total, Succeeded: job.Succeeded, Failed: job.Failed,
		Downloads: []downloadItem{}, Errors: []errorItem{},
	}
	for _, d := range docs {
		if d.Status == "succeeded" {
			tok, _ := s.Signer.SignDocument(d.ID)
			p.Downloads = append(p.Downloads, downloadItem{
				Filename: d.Filename,
				SignedURL: s.Cfg.ExternalBaseURL + "/api/v1/s/" + tok,
			})
		} else if d.Status == "failed" {
			p.Errors = append(p.Errors, errorItem{RecordIndex: d.RecordIndex, Error: d.Error, Path: d.PathErr})
		}
	}
	return p, nil
}
