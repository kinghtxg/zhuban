package api

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"docforge/server/internal/auth"
	"docforge/server/internal/models"
	"docforge/server/internal/render"

	"github.com/go-chi/chi/v5"
)

// --- 签名/印章素材库（V3 §6.2）---

// sigList GET /signatures 与 /seals 的通用列表，返回 {items,total}。
// 支持 query：keyword（name 模糊）、status、source、page、page_size。
func (s *Server) sigList(w http.ResponseWriter, r *http.Request, resource string) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	q := s.DB.Model(&models.Signature{})
	if resource == "seal" {
		q = s.DB.Model(&models.Seal{})
	}
	q = q.Where("deleted_at IS NULL")
	if !p.User.IsAdmin() {
		q = q.Where("owner_id = ?", p.User.ID)
	}
	if status := r.URL.Query().Get("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	if src := r.URL.Query().Get("source"); src != "" {
		q = q.Where("source = ?", src)
	}
	if kw := strings.TrimSpace(r.URL.Query().Get("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ?", like)
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}
	var total int64
	q.Count(&total)
	q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize)

	var out []map[string]interface{}
	if resource == "seal" {
		var rows []models.Seal
		q.Find(&rows)
		out = make([]map[string]interface{}, 0, len(rows))
		for i := range rows {
			out = append(out, sealMap(&rows[i]))
		}
	} else {
		var rows []models.Signature
		q.Find(&rows)
		out = make([]map[string]interface{}, 0, len(rows))
		for i := range rows {
			out = append(out, sigMap(&rows[i]))
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": out, "total": total})
}

func sigMap(x *models.Signature) map[string]interface{} {
	return map[string]interface{}{
		"id": x.ID, "name": x.Name, "owner_id": x.OwnerID, "source": x.Source,
		"signer_name": x.SignerName, "font_id": x.FontID, "params": jsonOrNil(x.Params),
		"width_mm": x.WidthMM, "height_mm": x.HeightMM, "status": x.Status,
		"reviewed_by": x.ReviewedBy, "reviewed_at": x.ReviewedAt, "review_note": x.ReviewNote,
		"created_at": x.CreatedAt,
		"url": "/api/v1/signatures/" + x.ID + "/file",
	}
}
func sealMap(x *models.Seal) map[string]interface{} {
	m := map[string]interface{}{
		"id": x.ID, "name": x.Name, "owner_id": x.OwnerID, "source": x.Source,
		"unit_name": x.UnitName, "seal_name": x.SealName, "shape": x.Shape, "params": jsonOrNil(x.Params),
		"width_mm": x.WidthMM, "height_mm": x.HeightMM, "status": x.Status,
		"reviewed_by": x.ReviewedBy, "reviewed_at": x.ReviewedAt, "review_note": x.ReviewNote,
		"created_at": x.CreatedAt,
		"url":        "/api/v1/seals/" + x.ID + "/file",
	}
	// radius_mm 与 star 以前既没入库也没返回，前端类型里声明了这两个字段、
	// 页面上却只能显示「—」/一律显示「有」。
	//
	// 半径是推出来的，不是独立存储的：创建时后端把表单里的 radius_mm
	// 换算成 width_mm = radius*2 存下来。所以这里反推回去，
	// 存量数据（params 里没有半径）也能正确显示。
	if x.WidthMM > 0 {
		m["radius_mm"] = x.WidthMM / 2
	}
	m["star"] = sealStarOf(x.Params)
	return m
}

// sealStarOf 从 params 里读五角星开关，读不到就按渲染服务的默认值（有星）。
// 存量印章的 params 里没有这个键——它们创建时确实按"有星"渲染了，
// 所以默认 true 与事实一致，不会让老数据看起来被改过。
func sealStarOf(params models.JSONB) bool {
	if len(params) == 0 {
		return true
	}
	var m map[string]interface{}
	if json.Unmarshal(params, &m) != nil {
		return true
	}
	v, ok := m["star"]
	if !ok {
		return true
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t != "false" && t != "0"
	}
	return true
}

func (s *Server) handleListSignatures(w http.ResponseWriter, r *http.Request) { s.sigList(w, r, "signature") }
func (s *Server) handleListSeals(w http.ResponseWriter, r *http.Request)    { s.sigList(w, r, "seal") }

// loadOwned 取本人（或 admin 任意）资源。
func (s *Server) loadOwned(w http.ResponseWriter, r *http.Request, resource string) (*auth.Principal, *models.Signature, *models.Seal) {
	p := s.requireUser(w, r)
	if p == nil {
		return nil, nil, nil
	}
	id := chi.URLParam(r, "id")
	if resource == "seal" {
		var x models.Seal
		if err := s.DB.First(&x, "id = ? AND deleted_at IS NULL", id).Error; err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "印章不存在", nil)
			return nil, nil, nil
		}
		if !p.User.IsAdmin() && x.OwnerID != p.User.ID {
			writeErr(w, http.StatusForbidden, "forbidden", "无权访问他人印章", nil)
			return nil, nil, nil
		}
		return p, nil, &x
	}
	var x models.Signature
	if err := s.DB.First(&x, "id = ? AND deleted_at IS NULL", id).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "签名不存在", nil)
		return nil, nil, nil
	}
	if !p.User.IsAdmin() && x.OwnerID != p.User.ID {
		writeErr(w, http.StatusForbidden, "forbidden", "无权访问他人签名", nil)
		return nil, nil, nil
	}
	return p, &x, nil
}

func (s *Server) handleGetSignature(w http.ResponseWriter, r *http.Request) {
	p, x, _ := s.loadOwned(w, r, "signature")
	if p == nil {
		return
	}
	writeJSON(w, http.StatusOK, sigMap(x))
}
func (s *Server) handleGetSeal(w http.ResponseWriter, r *http.Request) {
	p, _, x := s.loadOwned(w, r, "seal")
	if p == nil {
		return
	}
	writeJSON(w, http.StatusOK, sealMap(x))
}

// sigFile GET /signatures/{id}/file 与 /seals/{id}/file：PNG 下载。
// 可见范围见 canPreviewAsset：所有者、管理员、以及持有有效授权的人。
func (s *Server) sigFile(w http.ResponseWriter, r *http.Request, resource string) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	id := chi.URLParam(r, "id")
	var path, ownerID, status string
	if resource == "seal" {
		var x models.Seal
		if err := s.DB.First(&x, "id = ? AND deleted_at IS NULL", id).Error; err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "印章不存在", nil)
			return
		}
		path, ownerID, status = x.FilePath, x.OwnerID, x.Status
	} else {
		var x models.Signature
		if err := s.DB.First(&x, "id = ? AND deleted_at IS NULL", id).Error; err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "签名不存在", nil)
			return
		}
		path, ownerID, status = x.FilePath, x.OwnerID, x.Status
	}
	if !s.canPreviewAsset(p, resource, id, ownerID, status) {
		writeErr(w, http.StatusForbidden, "forbidden", "无权访问", nil)
		return
	}
	data, err := readFileSafe(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "文件不存在", nil)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(data)
}

func (s *Server) handleSignatureFile(w http.ResponseWriter, r *http.Request) { s.sigFile(w, r, "signature") }
func (s *Server) handleSealFile(w http.ResponseWriter, r *http.Request)    { s.sigFile(w, r, "seal") }

// reviewStatusAfterCreate 依据开关决定新建后的状态。
func (s *Server) reviewStatusAfterCreate(resource string) string {
	on := s.settingsBool("review_signatures_enabled")
	if resource == "seal" {
		on = s.settingsBool("review_seals_enabled")
	}
	if on {
		return "pending"
	}
	return "approved"
}

// createReviewRow 写一条审核流水。
func (s *Server) createReviewRow(resourceType, resourceID, action, operatorID, note string) {
	row := models.Review{
		ID: "rev_" + randID(), ResourceType: resourceType, ResourceID: resourceID,
		Action: action, OperatorID: operatorID, Note: note, CreatedAt: time.Now(),
	}
	_ = s.DB.Create(&row).Error
}

// createSignature POST /signatures（multipart：file 上传 或 生成参数）。
func (s *Server) handleCreateSignature(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	maxBytes := int64(s.Cfg.MaxImageMB) * 1024 * 1024
	_ = r.ParseMultipartForm(maxBytes + 4096)
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		writeErr(w, http.StatusBadRequest, "data_invalid", "name 必填", nil)
		return
	}
	src := r.FormValue("source")
	if src == "" {
		src = "upload"
	}
	id := "sig_" + randID()
	var png []byte
	widthMM, heightMM := s.settingsFloat("signature_default_width_mm", 60), 0.0
	params := map[string]interface{}{}
	if src == "generated" {
		fields := map[string]string{
			"signer_name": r.FormValue("signer_name"),
			"font_id":     r.FormValue("font_id"),
		}
		if r.FormValue("with_timestamp") != "" {
			fields["with_timestamp"] = r.FormValue("with_timestamp")
		}
		if r.FormValue("composite") != "" {
			fields["composite"] = r.FormValue("composite")
		}
		if r.FormValue("width_mm") != "" {
			fields["width_mm"] = r.FormValue("width_mm")
		}
		b, err := s.Renderer.RenderSignature(r.Context(), fields)
		if err != nil {
			s.renderErr(w, err)
			return
		}
		png = b
		params = map[string]interface{}{
			"signer_name": fields["signer_name"], "font_id": fields["font_id"],
		}
		if v, err := strconv.ParseFloat(r.FormValue("width_mm"), 64); err == nil && v > 0 {
			widthMM = v
		}
	} else {
		f, fh, err := r.FormFile("file")
		if err != nil {
			writeErr(w, http.StatusBadRequest, "data_invalid", "upload 来源需提供 file", nil)
			return
		}
		defer f.Close()
		body, _ := readAllLimit(f, maxBytes+1)
		if mime := sniffImageMIME(body); mime != "image/png" {
			writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "签名图仅支持 PNG", nil)
			return
		}
		_ = fh
		png = body
		widthMM, heightMM = imageDimsMM(body)
	}
	path := s.Store.SigPath(id)
	if err := s.Store.WriteFile(path, png, 0o644); err != nil {
		s.renderErr(w, err)
		return
	}
	status := s.reviewStatusAfterCreate("signature")
	paramsB, _ := json.Marshal(params)
	row := models.Signature{
		ID: id, Name: name, OwnerID: p.User.ID, Source: src,
		SignerName: r.FormValue("signer_name"), FontID: r.FormValue("font_id"),
		Params: paramsB, FilePath: path, WidthMM: widthMM, HeightMM: heightMM,
		Status: status,
	}
	if err := s.DB.Create(&row).Error; err != nil {
		s.renderErr(w, err)
		return
	}
	s.createReviewRow("signature", id, "submit", p.User.ID, "")
	s.auditReq(r, p, "signature.create", "signature", id, map[string]interface{}{"name": name, "status": status, "source": src})
	writeJSON(w, http.StatusCreated, sigMap(&row))
}

// createSeal POST /seals。
func (s *Server) handleCreateSeal(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	maxBytes := int64(s.Cfg.MaxImageMB) * 1024 * 1024
	_ = r.ParseMultipartForm(maxBytes + 4096)
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		writeErr(w, http.StatusBadRequest, "data_invalid", "name 必填", nil)
		return
	}
	src := r.FormValue("source")
	if src == "" {
		src = "upload"
	}
	id := "seal_" + randID()
	var png []byte
	widthMM, heightMM := s.settingsFloat("seal_default_width_mm", 40), 0.0
	params := map[string]interface{}{}
	if src == "generated" {
		fields := map[string]string{
			"unit_name": r.FormValue("unit_name"),
			"seal_name": r.FormValue("seal_name"),
			"shape":     r.FormValue("shape"),
		}
		if r.FormValue("radius_mm") != "" {
			fields["radius_mm"] = r.FormValue("radius_mm")
		}
		if r.FormValue("star") != "" {
			fields["star"] = r.FormValue("star")
		}
		b, err := s.Renderer.RenderSeal(r.Context(), fields)
		if err != nil {
			s.renderErr(w, err)
			return
		}
		png = b
		// params 要存全：以前只存了 unit_name / seal_name / shape，
		// 于是「五角星关掉」和「自定义半径」这两个选择在入库后就丢了，
		// 详情页只能显示默认值。渲染当时是对的（fields 里有），
		// 但任何"按存下来的参数再看一眼"的场景都会与实际图不一致。
		params = map[string]interface{}{
			"unit_name": fields["unit_name"], "seal_name": fields["seal_name"], "shape": fields["shape"],
		}
		if v, ok := fields["star"]; ok {
			params["star"] = v
		}
		if v, ok := fields["radius_mm"]; ok {
			params["radius_mm"] = v
		}
		if v, err := strconv.ParseFloat(r.FormValue("radius_mm"), 64); err == nil && v > 0 {
			widthMM = v * 2
		}
	} else {
		f, fh, err := r.FormFile("file")
		if err != nil {
			writeErr(w, http.StatusBadRequest, "data_invalid", "upload 来源需提供 file", nil)
			return
		}
		defer f.Close()
		body, _ := readAllLimit(f, maxBytes+1)
		if mime := sniffImageMIME(body); mime != "image/png" {
			writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "印章图仅支持 PNG", nil)
			return
		}
		_ = fh
		png = body
		widthMM, heightMM = imageDimsMM(body)
	}
	path := s.Store.SealPath(id)
	if err := s.Store.WriteFile(path, png, 0o644); err != nil {
		s.renderErr(w, err)
		return
	}
	status := s.reviewStatusAfterCreate("seal")
	paramsB, _ := json.Marshal(params)
	row := models.Seal{
		ID: id, Name: name, OwnerID: p.User.ID, Source: src,
		UnitName: r.FormValue("unit_name"), SealName: r.FormValue("seal_name"), Shape: r.FormValue("shape"),
		Params: paramsB, FilePath: path, WidthMM: widthMM, HeightMM: heightMM,
		Status: status,
	}
	if err := s.DB.Create(&row).Error; err != nil {
		s.renderErr(w, err)
		return
	}
	s.createReviewRow("seal", id, "submit", p.User.ID, "")
	s.auditReq(r, p, "seal.create", "seal", id, map[string]interface{}{"name": name, "status": status, "source": src})
	writeJSON(w, http.StatusCreated, sealMap(&row))
}

// deleteSignature/Seal：软删。
func (s *Server) handleDeleteSignature(w http.ResponseWriter, r *http.Request) {
	p, x, _ := s.loadOwned(w, r, "signature")
	if p == nil {
		return
	}
	s.softDeleteAsset(w, r, p, "signature", x.ID, x.Name, func() error {
		now := time.Now()
		return s.DB.Model(x).Update("deleted_at", &now).Error
	})
}
func (s *Server) handleDeleteSeal(w http.ResponseWriter, r *http.Request) {
	p, _, x := s.loadOwned(w, r, "seal")
	if p == nil {
		return
	}
	s.softDeleteAsset(w, r, p, "seal", x.ID, x.Name, func() error {
		now := time.Now()
		return s.DB.Model(x).Update("deleted_at", &now).Error
	})
}

// softDeleteAsset 软删签章素材，并吊销所有指向它的授权。
//
// 为什么要级联吊销：资源没了，指向它的 Grant 就只是一条悬空记录。虽然
// sigauth.resolveOne 会因 deleted_at 拒用（用不出问题），但授权抽屉里会显示
// 「已授权给 3 个成员」的幽灵条目，管理员会以为还在生效，反复核对。
// 资源已删 → 授权本就该失效，不需要留着。
func (s *Server) softDeleteAsset(w http.ResponseWriter, r *http.Request, p *auth.Principal,
	resourceType, resourceID, name string, do func() error) {
	if err := do(); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "删除失败："+err.Error(), nil)
		return
	}
	var revoked int64
	if res := s.DB.Model(&models.Grant{}).
		Where("resource_type = ? AND resource_id = ?", resourceType, resourceID).
		Delete(&models.Grant{}); res.Error == nil {
		revoked = res.RowsAffected
	}
	s.auditReq(r, p, resourceType+".delete", resourceType, resourceID, map[string]interface{}{
		"name": name, "revoked_grants": revoked,
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "revoked_grants": revoked})
}

// render-preview：不落库不审计，直接透传 render-service。
func (s *Server) handleRenderSignaturePreview(w http.ResponseWriter, r *http.Request) {
	if s.requireUser(w, r) == nil {
		return
	}
	_ = r.ParseMultipartForm(1 << 20)
	fields := map[string]string{}
	for _, k := range []string{"signer_name", "font_id", "with_timestamp", "composite", "width_mm"} {
		if v := r.FormValue(k); v != "" {
			fields[k] = v
		}
	}
	b, err := s.Renderer.RenderSignature(r.Context(), fields)
	if err != nil {
		s.renderErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(b)
}
func (s *Server) handleRenderSealPreview(w http.ResponseWriter, r *http.Request) {
	if s.requireUser(w, r) == nil {
		return
	}
	_ = r.ParseMultipartForm(1 << 20)
	fields := map[string]string{}
	for _, k := range []string{"unit_name", "seal_name", "shape", "radius_mm", "star"} {
		if v := r.FormValue(k); v != "" {
			fields[k] = v
		}
	}
	b, err := s.Renderer.RenderSeal(r.Context(), fields)
	if err != nil {
		s.renderErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(b)
}

// handleFonts GET /render/fonts 代理。
func (s *Server) handleFonts(w http.ResponseWriter, r *http.Request) {
	if s.requireUser(w, r) == nil {
		return
	}
	fonts, err := s.Renderer.Fonts(r.Context())
	if err != nil {
		s.renderErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"fonts": fonts})
}

// --- 审核状态机（V3 §4）---

// handleReviewQueue GET /review/queue。
func (s *Server) handleReviewQueue(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var sigPending, sealPending int64
	s.DB.Model(&models.Signature{}).Where("status = ? AND deleted_at IS NULL", "pending").Count(&sigPending)
	s.DB.Model(&models.Seal{}).Where("status = ? AND deleted_at IS NULL", "pending").Count(&sealPending)
	items := []map[string]interface{}{}
	add := func(resourceType string, rows interface{}) {
		switch rr := rows.(type) {
		case []models.Signature:
			for i := range rr {
				items = append(items, map[string]interface{}{
					"resource_type": "signature", "id": rr[i].ID, "name": rr[i].Name,
					"status": rr[i].Status, "owner_id": rr[i].OwnerID, "created_at": rr[i].CreatedAt,
				})
			}
		case []models.Seal:
			for i := range rr {
				items = append(items, map[string]interface{}{
					"resource_type": "seal", "id": rr[i].ID, "name": rr[i].Name,
					"status": rr[i].Status, "owner_id": rr[i].OwnerID, "created_at": rr[i].CreatedAt,
				})
			}
		}
	}
	if rt := r.URL.Query().Get("resource_type"); rt == "" || rt == "signature" {
		var rows []models.Signature
		s.DB.Where("status = ? AND deleted_at IS NULL", "pending").Order("created_at ASC").Find(&rows)
		add("signature", rows)
		if rt == "" || rt == "seal" {
			var rows2 []models.Seal
			s.DB.Where("status = ? AND deleted_at IS NULL", "pending").Order("created_at ASC").Find(&rows2)
			add("seal", rows2)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": items,
		"pending": map[string]int64{"signature": sigPending, "seal": sealPending},
	})
}

// reviewAction POST /review/{resource_type}/{id}/{approve|reject}。
func (s *Server) reviewAction(w http.ResponseWriter, r *http.Request, action string) {
	p := s.requireAdmin(w, r)
	if p == nil {
		return
	}
	rt := chi.URLParam(r, "resource_type")
	id := chi.URLParam(r, "id")
	// 同时支持 JSON body（{note}）与 form。
	note := r.FormValue("note")
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
		var jn struct {
			Note string `json:"note"`
		}
		if err := json.Unmarshal(body, &jn); err == nil && jn.Note != "" {
			note = jn.Note
		}
	}
	if action == "reject" && strings.TrimSpace(note) == "" {
		writeErr(w, http.StatusBadRequest, "data_invalid", "驳回 note 必填", nil)
		return
	}
	now := time.Now()
	if rt == "signature" {
		var x models.Signature
		if err := s.DB.First(&x, "id = ? AND deleted_at IS NULL", id).Error; err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "签名不存在", nil)
			return
		}
		newStatus := "approved"
		if action == "reject" {
			newStatus = "rejected"
		}
		s.DB.Model(&x).Updates(map[string]interface{}{
			"status": newStatus, "reviewed_by": p.User.ID, "reviewed_at": &now, "review_note": note,
		})
		s.createReviewRow("signature", id, action, p.User.ID, note)
		s.auditReq(r, p, "review."+action, "signature", id, map[string]interface{}{"note": note, "name": x.Name})
	} else if rt == "seal" {
		var x models.Seal
		if err := s.DB.First(&x, "id = ? AND deleted_at IS NULL", id).Error; err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "印章不存在", nil)
			return
		}
		newStatus := "approved"
		if action == "reject" {
			newStatus = "rejected"
		}
		s.DB.Model(&x).Updates(map[string]interface{}{
			"status": newStatus, "reviewed_by": p.User.ID, "reviewed_at": &now, "review_note": note,
		})
		s.createReviewRow("seal", id, action, p.User.ID, note)
		s.auditReq(r, p, "review."+action, "seal", id, map[string]interface{}{"note": note, "name": x.Name})
	} else {
		writeErr(w, http.StatusBadRequest, "data_invalid", "resource_type 仅支持 signature|seal", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (s *Server) handleReviewApprove(w http.ResponseWriter, r *http.Request) { s.reviewAction(w, r, "approve") }
func (s *Server) handleReviewReject(w http.ResponseWriter, r *http.Request) {
	s.reviewAction(w, r, "reject")
}

// updateSignature PUT /signatures/{id}：改名 / 重传 / 重新生成 / 启用。
func (s *Server) handleUpdateSignature(w http.ResponseWriter, r *http.Request) {
	p, x, _ := s.loadOwned(w, r, "signature")
	if p == nil {
		return
	}
	maxBytes := int64(s.Cfg.MaxImageMB) * 1024 * 1024
	_ = r.ParseMultipartForm(maxBytes + 4096)
	upd := map[string]interface{}{}
	if name := strings.TrimSpace(r.FormValue("name")); name != "" {
		upd["name"] = name
	}
	changedFile := false
	// 重传图片
	if f, fh, err := r.FormFile("file"); err == nil {
		defer f.Close()
		body, _ := readAllLimit(f, maxBytes+1)
		if sniffImageMIME(body) != "image/png" {
			writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "签名图仅支持 PNG", nil)
			return
		}
		_ = fh
		if werr := s.Store.WriteFile(x.FilePath, body, 0o644); werr != nil {
			s.renderErr(w, werr)
			return
		}
		w, h := imageDimsMM(body)
		upd["width_mm"], upd["height_mm"] = w, h
		upd["source"] = "upload"
		changedFile = true
	} else if r.FormValue("source") == "generated" {
		fields := map[string]string{}
		for _, k := range []string{"signer_name", "font_id", "with_timestamp", "composite", "width_mm"} {
			if v := r.FormValue(k); v != "" {
				fields[k] = v
				upd[k] = v
			}
		}
		b, gerr := s.Renderer.RenderSignature(r.Context(), fields)
		if gerr != nil {
			s.renderErr(w, gerr)
			return
		}
		if werr := s.Store.WriteFile(x.FilePath, b, 0o644); werr != nil {
			s.renderErr(w, werr)
			return
		}
		upd["source"] = "generated"
		changedFile = true
	}
	// 文件变更 → 按开关重置审核状态。
	if changedFile {
		status := s.reviewStatusAfterCreate("signature")
		upd["status"] = status
		s.createReviewRow("signature", x.ID, "submit", p.User.ID, "")
	}
	// 启用 disabled 资源。
	if r.FormValue("status") == "active" && x.Status == "disabled" {
		upd["status"] = "approved"
		s.createReviewRow("signature", x.ID, "enable", p.User.ID, "")
	}
	if err := s.DB.Model(x).Updates(upd).Error; err != nil {
		s.renderErr(w, err)
		return
	}
	s.DB.First(x, "id=?", x.ID)
	s.auditReq(r, p, "signature.update", "signature", x.ID, upd)
	writeJSON(w, http.StatusOK, sigMap(x))
}

// updateSeal PUT /seals/{id}。
func (s *Server) handleUpdateSeal(w http.ResponseWriter, r *http.Request) {
	p, _, x := s.loadOwned(w, r, "seal")
	if p == nil {
		return
	}
	maxBytes := int64(s.Cfg.MaxImageMB) * 1024 * 1024
	_ = r.ParseMultipartForm(maxBytes + 4096)
	upd := map[string]interface{}{}
	if name := strings.TrimSpace(r.FormValue("name")); name != "" {
		upd["name"] = name
	}
	changedFile := false
	if f, fh, err := r.FormFile("file"); err == nil {
		defer f.Close()
		body, _ := readAllLimit(f, maxBytes+1)
		if sniffImageMIME(body) != "image/png" {
			writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "印章图仅支持 PNG", nil)
			return
		}
		_ = fh
		if werr := s.Store.WriteFile(x.FilePath, body, 0o644); werr != nil {
			s.renderErr(w, werr)
			return
		}
		w, h := imageDimsMM(body)
		upd["width_mm"], upd["height_mm"] = w, h
		upd["source"] = "upload"
		changedFile = true
	} else if r.FormValue("source") == "generated" {
		fields := map[string]string{}
		for _, k := range []string{"unit_name", "seal_name", "shape", "radius_mm", "star"} {
			if v := r.FormValue(k); v != "" {
				fields[k] = v
				upd[k] = v
			}
		}
		b, gerr := s.Renderer.RenderSeal(r.Context(), fields)
		if gerr != nil {
			s.renderErr(w, gerr)
			return
		}
		if werr := s.Store.WriteFile(x.FilePath, b, 0o644); werr != nil {
			s.renderErr(w, werr)
			return
		}
		upd["source"] = "generated"
		changedFile = true
	}
	if changedFile {
		status := s.reviewStatusAfterCreate("seal")
		upd["status"] = status
		s.createReviewRow("seal", x.ID, "submit", p.User.ID, "")
	}
	if r.FormValue("status") == "active" && x.Status == "disabled" {
		upd["status"] = "approved"
		s.createReviewRow("seal", x.ID, "enable", p.User.ID, "")
	}
	if err := s.DB.Model(x).Updates(upd).Error; err != nil {
		s.renderErr(w, err)
		return
	}
	s.DB.First(x, "id=?", x.ID)
	s.auditReq(r, p, "seal.update", "seal", x.ID, upd)
	writeJSON(w, http.StatusOK, sealMap(x))
}

// 防止 import 未用。
var _ = io.EOF
var _ render.Font
var _ = os.Remove
