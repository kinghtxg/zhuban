package api

import (
	"net/http"

	"docforge/server/internal/models"
	"docforge/server/internal/sign"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// handleSignedDownload 签名直链下载：document→docx/pdf，job→zip。
func (s *Server) handleSignedDownload(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	parsed, err := s.Signer.Verify(token)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "链接无效或已过期", nil)
		return
	}
	switch parsed.Kind {
	case sign.KindDocument:
		var d models.Document
		if err := s.DB.First(&d, "id = ?", parsed.ID).Error; err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "文档不存在", nil)
			return
		}
		if d.Expired {
			writeErr(w, http.StatusGone, "expired", "文档已过期", nil)
			return
		}
		pdfRequested := r.URL.Query().Get("format") == "pdf"
		path := d.DocxPath
		ct := "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		switch {
		case pdfRequested && d.PdfPath != "":
			path = d.PdfPath
			ct = "application/pdf"
		case pdfRequested:
			writeErr(w, http.StatusGone, "expired", "该文档没有 PDF 版本", nil)
			return
		case d.DocxPath == "" && d.PdfPath != "":
			// 只生成了 PDF 的文档（format=pdf 投递），无 docx 时自动回落到 PDF
			path = d.PdfPath
			ct = "application/pdf"
		case d.DocxPath == "":
			writeErr(w, http.StatusGone, "expired", "文件不存在或已清理", nil)
			return
		}
		// 与 job 的整包下载同一处理方式：这条路径不需要登录，
		// 也就不受限流保护，整包读进内存更容易被打爆。
		err := serveFileDownload(w, r, path, d.Filename, ct, func() {
			s.DB.Model(&models.Document{}).Where("id = ?", d.ID).
				UpdateColumn("download_count", gorm.Expr("download_count + 1"))
		})
		if err != nil {
			writeErr(w, http.StatusGone, "expired", "文件不存在或已清理", nil)
			return
		}
	case sign.KindJob:
		var job models.Job
		if err := s.DB.First(&job, "id = ?", parsed.ID).Error; err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "任务不存在", nil)
			return
		}
		if job.Expired || job.ZipPath == "" {
			writeErr(w, http.StatusGone, "expired", "产物已过期", nil)
			return
		}
		// 直链下的整包下载同样走流式：这条路径不需要登录，
		// 也就没有限流兜着，整包读进内存更容易被打爆。
		if err := serveFileDownload(w, r, job.ZipPath, job.ID+".zip", "application/zip", nil); err != nil {
			writeErr(w, http.StatusGone, "expired", "文件不存在", nil)
			return
		}
	}
}
