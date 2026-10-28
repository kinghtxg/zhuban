package api

import (
	"context"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"docforge/server/internal/auditx"
	"docforge/server/internal/auth"
	"docforge/server/internal/models"
)

// chain 返回审计链写入器。
func (s *Server) chain() *auditx.Chain { return &auditx.Chain{DB: s.DB} }

// audit 写一条哈希链审计日志（V3）。失败只记日志，不阻断主流程。
func (s *Server) audit(p *auth.Principal, action, objectType, objectID string, detail map[string]interface{}) {
	s.auditReq(nil, p, action, objectType, objectID, detail)
}

// auditReq 同 audit，但从 r 取 source_ip 与 endpoint。
func (s *Server) auditReq(r *http.Request, p *auth.Principal, action, objectType, objectID string, detail map[string]interface{}) {
	e := &auditx.Entry{
		Action: action, ObjectType: objectType, ObjectID: objectID,
		Detail: detail,
	}
	if p != nil {
		if p.User != nil {
			e.ActorType = "user"
			e.ActorUserID = p.User.ID
			e.ActorName = p.User.DisplayName
			if e.ActorName == "" {
				e.ActorName = p.User.Username
			}
		} else if p.APIKey != nil {
			e.ActorType = "api_key"
			e.ActorName = p.APIKey.Name
		}
	}
	if r != nil {
		e.SourceIP = s.clientIP(r)
		e.Endpoint = r.Method + " " + r.URL.Path
	}
	if _, err := s.chain().Write(e); err != nil {
		log.Printf("[audit] chain write failed: %v", err)
	}
}

// handleAuditLogs 审计日志查询：admin 全量；普通用户仅看自己作为操作人或与自己对象相关的记录。
func (s *Server) handleAuditLogs(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	q := s.DB.Model(&models.AuditLog{})
	if !p.User.IsAdmin() {
		// 普通用户：只看自己操作产生的审计（对象维度的"相关"一期收敛为本人操作）。
		q = q.Where("actor_user_id = ?", p.User.ID)
	}
	if ot := r.URL.Query().Get("object_type"); ot != "" {
		q = q.Where("object_type = ?", ot)
	}
	if oid := r.URL.Query().Get("object_id"); oid != "" {
		q = q.Where("object_id = ?", oid)
	}
	if act := r.URL.Query().Get("action"); act != "" {
		q = q.Where("action LIKE ?", "%"+act+"%")
	}
	var total int64
	q.Count(&total)
	pg := getPage(r)
	var rows []models.AuditLog
	q.Order("created_at DESC").Offset((pg.Page - 1) * pg.PageSize).Limit(pg.PageSize).Find(&rows)
	items := make([]map[string]interface{}, 0, len(rows))
	for i := range rows {
		items = append(items, map[string]interface{}{
			"id": rows[i].ID, "seq": rows[i].Seq,
			"actor_type": rows[i].ActorType, "actor_user_id": rows[i].ActorUserID, "actor_name": rows[i].ActorName,
			"action": rows[i].Action, "object_type": rows[i].ObjectType, "object_id": rows[i].ObjectID,
			"template_code": rows[i].TemplateCode, "external_ref": rows[i].ExternalRef,
			"signature_id": rows[i].SignatureID, "seal_id": rows[i].SealID,
			"source_ip": rows[i].SourceIP, "endpoint": rows[i].Endpoint,
			"detail": jsonOrNil(rows[i].Detail),
			"occurred_at": rows[i].OccurredAt, "created_at": rows[i].CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items, "total": total})
}

// handleAuditLogsAdmin /admin/audit-logs：admin 全量多维筛选。
func (s *Server) handleAuditLogsAdmin(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	s.handleAuditLogs(w, r)
}

// handleAdminSettings 只读非涉密系统设置（PRD §4.12）。密钥类字段一律不返回。
func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	resp := map[string]interface{}{
		"retention_days":          s.Cfg.RetentionDays,
		"signed_url_ttl_hours":    int(s.Cfg.SignedURLTTL / time.Hour),
		"max_records":             s.Cfg.MaxRecords,
		"max_json_mb":              s.Cfg.MaxJSONMB,
		"max_template_mb":         s.Cfg.MaxTemplateMB,
		"max_image_mb":            s.Cfg.MaxImageMB,
		"callback_allow_private":   s.Cfg.CallbackAllowPrivate,
		"callback_host_allowlist": s.Cfg.CallbackHostAllow,
		"oidc_enabled":             s.Cfg.OIDCEnabled,
		"allow_register":          s.Cfg.AllowRegister,
	}
	// render 健康状态（用现有探测）。
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	h, err := s.Renderer.Health(ctx)
	if err != nil {
		resp["render"] = map[string]interface{}{"status": "error", "error": err.Error()}
	} else {
		resp["render"] = h
	}
	for k, v := range s.v3Settings() {
		resp[k] = v
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAdminSettingsPut 更新可在线修改的设置（V3）。密钥类不可改。
func (s *Server) handleAdminSettingsPut(w http.ResponseWriter, r *http.Request) {
	p := s.requireAdmin(w, r)
	if p == nil {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := s.upsertSettings(body); err != nil {
		s.renderErr(w, err)
		return
	}
	s.auditReq(r, p, "settings.update", "settings", "global", map[string]interface{}{"keys": string(body)})
	writeJSON(w, http.StatusOK, map[string]interface{}{"settings": s.v3Settings()})
}

// handleAuditVerify 全链重算。
func (s *Server) handleAuditVerify(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	ok, checked, brokenSeq, expected, actual := s.chain().Verify()
	resp := map[string]interface{}{"ok": ok, "checked": checked}
	if !ok {
		resp["broken_at_seq"] = brokenSeq
		resp["expected_hash"] = expected
		resp["actual_hash"] = actual
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAuditExport 导出审计 CSV（含链校验头）。
func (s *Server) handleAuditExport(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var rows []models.AuditLog
	q := s.DB.Model(&models.AuditLog{})
	if ot := r.URL.Query().Get("object_type"); ot != "" {
		q = q.Where("object_type = ?", ot)
	}
	if oid := r.URL.Query().Get("object_id"); oid != "" {
		q = q.Where("object_id = ?", oid)
	}
	q.Order("seq ASC").Find(&rows)
	var prevHash, lastHash string
	if n := len(rows); n > 0 {
		prevHash = rows[0].PrevHash
		lastHash = rows[n-1].SelfHash
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="audit.csv"`)
	_, _ = w.Write([]byte("# zhuban audit export\n"))
	_, _ = w.Write([]byte("# range_start_prev_hash=" + prevHash + "\n"))
	_, _ = w.Write([]byte("# range_end_self_hash=" + lastHash + "\n"))
	_, _ = w.Write([]byte("# count=" + itoa(len(rows)) + "\n"))
	_, _ = w.Write([]byte("# exported_at=" + time.Now().UTC().Format(time.RFC3339) + "\n"))
	_, _ = w.Write([]byte("seq,actor_type,actor_user_id,actor_name,action,object_type,object_id,occurred_at,prev_hash,self_hash\n"))
	for i := range rows {
		row := rows[i]
		line := []string{
			itoa(int(row.Seq)), row.ActorType, row.ActorUserID, row.ActorName,
			row.Action, row.ObjectType, row.ObjectID,
			row.OccurredAt.UTC().Format(time.RFC3339),
			row.PrevHash, row.SelfHash,
		}
		for j, c := range line {
			if j > 0 {
				_, _ = w.Write([]byte(","))
			}
			_, _ = w.Write([]byte(csvField(c)))
		}
		_, _ = w.Write([]byte("\n"))
	}
}

// csvField 极简 CSV 转义 + 公式中和。
//
// 两件事，缺一不可：
//
//  1. 结构转义：含逗号/引号/换行/回车都要加引号并把引号翻倍。回车单独列出来，
//     因为只处理 \n 的话，"a\rb" 会被原样写进一个字段，而 Excel 把裸 \r 也当换行，
//     结果是这一行整体错位——导出的审计日志行数与库里的对不上，还没人知道为什么。
//
//  2. 公式中和：单元格以 = + - @ 或制表符/回车开头时，Excel / WPS / Sheets
//     会当公式执行。审计日志里的 actor_name、object_id 全由用户可控
//     （改个显示名就行），导出后被管理员用表格软件打开，就是一次代码执行。
//     加前导单引号是最通用的中和方式（表格软件会把整格当文本）。
//
// 注意：这一步只作用于导出文件，不改善库里存的值，也不参与哈希链计算——
// 审计链校验仍然按原始字节比对，不会因为导出转义而对不上。
func csvField(s string) string {
	need := strings.ContainsAny(s, ",\"\n\r")
	formula := s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0]))
	if formula {
		s = "'" + s
	}
	if !need && !formula {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
