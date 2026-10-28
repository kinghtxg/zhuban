package api

import (
	"context"
	"net/http"
	"time"

	"docforge"
)

// handleHealthz 健康检查：db 探测由 GORM 隐式完成，这里探 render。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	dbStatus := "ok"
	if sqlDB, err := s.DB.DB(); err != nil || sqlDB.Ping() != nil {
		dbStatus = "error"
	}
	renderStatus := "ok"
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if _, err := s.Renderer.Health(ctx); err != nil {
		renderStatus = "error"
	}
	status := "ok"
	code := http.StatusOK
	if dbStatus != "ok" || renderStatus != "ok" {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]interface{}{"status": status, "db": dbStatus, "render": renderStatus})
}

// handleBlankTemplate 下载内嵌空白示例模板。
func (s *Server) handleBlankTemplate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	w.Header().Set("Content-Disposition", `attachment; filename="blank-template.docx"`)
	_, _ = w.Write(docforge.BlankTemplateDOCX)
}
