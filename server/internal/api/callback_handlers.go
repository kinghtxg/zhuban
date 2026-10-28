package api

import (
	"net/http"

	"docforge/server/internal/models"

	"github.com/go-chi/chi/v5"
)

// handleCallbackLogs GET /callback-logs
// admin 全量；普通用户仅看自己发起任务的回调记录。支持 job_id 过滤与分页。
func (s *Server) handleCallbackLogs(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	q := s.DB.Model(&models.CallbackLog{})
	if jid := r.URL.Query().Get("job_id"); jid != "" {
		q = q.Where("job_id = ?", jid)
	}
	// 非 admin：仅保留自己创建的任务的回调。
	if !p.User.IsAdmin() {
		q = q.Joins("JOIN jobs ON jobs.id = callback_logs.job_id").
			Where("jobs.created_by_user_id = ?", p.User.ID)
	}
	var total int64
	q.Count(&total)
	pg := getPage(r)
	var rows []models.CallbackLog
	q.Order("callback_logs.created_at DESC").
		Offset((pg.Page - 1) * pg.PageSize).Limit(pg.PageSize).Find(&rows)
	items := make([]map[string]interface{}, 0, len(rows))
	for i := range rows {
		items = append(items, map[string]interface{}{
			"id": rows[i].ID, "job_id": rows[i].JobID, "callback_url": rows[i].CallbackURL,
			"status": rows[i].Status, "attempts": rows[i].Attempts,
			"error_summary": rows[i].ErrorSummary, "created_at": rows[i].CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items, "total": total})
}

// handleReplayCallback POST /jobs/{id}/callbacks/replay
// 重放一次回调。admin 或任务属主可用。
func (s *Server) handleReplayCallback(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	var job models.Job
	if err := s.DB.First(&job, "id = ?", chi.URLParam(r, "id")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "任务不存在", nil)
		return
	}
	if !p.User.IsAdmin() && job.CreatedByUserID != p.User.ID {
		writeErr(w, http.StatusForbidden, "forbidden", "仅管理员或任务属主可重放回调", nil)
		return
	}
	if job.CallbackURL == "" {
		writeErr(w, http.StatusBadRequest, "data_invalid", "该任务未配置回调地址", nil)
		return
	}
	s.Sender.SendAsync(&job)
	s.audit(p, "callback.replay", "job", job.ID, map[string]interface{}{"callback_url": job.CallbackURL})
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "status": job.CallbackStatus})
}
