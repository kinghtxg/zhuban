package api

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"strings"

	"docforge/server/internal/models"

	"github.com/go-chi/chi/v5"
)

var imageExtByMIME = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// handleListAssets GET /templates/{id}/assets
func (s *Server) handleListAssets(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	tplID := chi.URLParam(r, "id")
	var tpl models.Template
	if err := s.DB.First(&tpl, "id = ?", tplID).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "模板不存在", nil)
		return
	}
	var assets []models.TemplateAsset
	s.DB.Where("template_id = ?", tplID).Order("created_at DESC").Find(&assets)
	items := make([]map[string]interface{}, 0, len(assets))
	for i := range assets {
		items = append(items, assetMap(&assets[i]))
	}
	// 前端按 TemplateAsset[] 直接消费（非分页包裹）。
	writeJSON(w, http.StatusOK, items)
}

// handleUploadAsset POST /templates/{id}/assets（multipart file+name+type）。
func (s *Server) handleUploadAsset(w http.ResponseWriter, r *http.Request) {
	p, tpl := s.loadEditableTemplate(w, r)
	if p == nil {
		return
	}
	maxBytes := int64(s.Cfg.MaxImageMB) * 1024 * 1024
	if err := r.ParseMultipartForm(maxBytes + 1024); err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large", "资源过大", nil)
		return
	}
	f, fh, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "缺少资源文件 file", nil)
		return
	}
	defer f.Close()
	body, _ := readAllLimit(f, maxBytes+1)
	if int64(len(body)) > maxBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large",
			"图片不能超过 "+itoa(s.Cfg.MaxImageMB)+"MB", nil)
		return
	}
	// 嗅探 MIME，仅允许白名单图片类型。
	mime := sniffImageMIME(body)
	if mime == "" {
		writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type",
			"仅支持 png/jpg/gif/webp 图片", nil)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = fh.Filename
	}
	typ := r.FormValue("type")
	if typ == "" {
		typ = "image"
	}
	// 解码宽高（像素，按 96dpi 折算 mm；失败则留 0）。
	widthMM, heightMM := imageDimsMM(body)
	ext := imageExtByMIME[mime]
	aid := "ast_" + randID()
	path := s.Store.AssetPath(tpl.ID, aid, ext)
	// Store.WriteFile 会自动 MkdirAll 父目录（首次上传 assets 目录不存在时不再 500）。
	if werr := s.Store.WriteFile(path, body, 0o644); werr != nil {
		s.renderErr(w, werr)
		return
	}
	asset := models.TemplateAsset{
		ID: aid, TemplateID: tpl.ID, Name: name, Type: typ, FilePath: path,
		MIME: mime, WidthMM: widthMM, HeightMM: heightMM, CreatedBy: p.User.ID,
	}
	if cerr := s.DB.Create(&asset).Error; cerr != nil {
		s.renderErr(w, cerr)
		return
	}
	s.audit(p, "asset.upload", "asset", asset.ID, map[string]interface{}{"template_id": tpl.ID, "name": name})
	writeJSON(w, http.StatusCreated, assetMap(&asset))
}

// handleDeleteAssets DELETE /templates/{id}/assets（清空该模板全部资源）。
func (s *Server) handleDeleteAssets(w http.ResponseWriter, r *http.Request) {
	p, tpl := s.loadEditableTemplate(w, r)
	if p == nil {
		return
	}
	var assets []models.TemplateAsset
	s.DB.Where("template_id = ?", tpl.ID).Find(&assets)
	for i := range assets {
		os.Remove(assets[i].FilePath)
	}
	s.DB.Where("template_id = ?", tpl.ID).Delete(&models.TemplateAsset{})
	s.audit(p, "asset.delete_all", "template", tpl.ID, map[string]interface{}{"count": len(assets)})
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "deleted": len(assets)})
}

// handleDeleteAsset DELETE /templates/{id}/assets/{aid}。
func (s *Server) handleDeleteAsset(w http.ResponseWriter, r *http.Request) {
	p, tpl := s.loadEditableTemplate(w, r)
	if p == nil {
		return
	}
	var asset models.TemplateAsset
	if err := s.DB.First(&asset, "id = ? AND template_id = ?", chi.URLParam(r, "aid"), tpl.ID).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "资源不存在", nil)
		return
	}
	os.Remove(asset.FilePath)
	s.DB.Delete(&asset)
	s.audit(p, "asset.delete", "asset", asset.ID, map[string]interface{}{"template_id": tpl.ID, "name": asset.Name})
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// handleAssetFile GET /templates/{id}/assets/{aid}/file（鉴权保护的图片下载）。
func (s *Server) handleAssetFile(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	var asset models.TemplateAsset
	if err := s.DB.First(&asset, "id = ? AND template_id = ?", chi.URLParam(r, "aid"), chi.URLParam(r, "id")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "资源不存在", nil)
		return
	}
	data, err := readFileSafe(asset.FilePath)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "资源文件不存在", nil)
		return
	}
	w.Header().Set("Content-Type", asset.MIME)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(data)
}

// assetMap 序列化为前端契约形状。
func assetMap(a *models.TemplateAsset) map[string]interface{} {
	return map[string]interface{}{
		"id": a.ID, "name": a.Name, "type": a.Type,
		"url": "/api/v1/templates/" + a.TemplateID + "/assets/" + a.ID + "/file",
		"width_mm": a.WidthMM, "height_mm": a.HeightMM,
		"created_by": a.CreatedBy, "created_at": a.CreatedAt,
	}
}

// sniffImageMIME 用内容嗅探判定图片类型，仅返回白名单。
func sniffImageMIME(b []byte) string {
	mime := http.DetectContentType(b)
	if _, ok := imageExtByMIME[mime]; ok {
		return mime
	}
	// webp 魔数：RIFF????WEBP
	if len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		return "image/webp"
	}
	return ""
}

// imageDimsMM 解码图片像素宽高并按 96dpi 折算毫米。
func imageDimsMM(b []byte) (float64, float64) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return 0, 0
	}
	const pxPerMM = 96.0 / 25.4
	return float64(cfg.Width) / pxPerMM, float64(cfg.Height) / pxPerMM
}

// readAllLimit 读至多 n+1 字节（超出则截断，调用方再判长）。
func readAllLimit(r interface{ Read([]byte) (int, error) }, n int64) ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	_, err := buf.ReadFrom(io.LimitReader(r, n+1))
	return buf.Bytes(), err
}
