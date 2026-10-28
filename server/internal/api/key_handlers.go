package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"docforge/server/internal/auth"
	"docforge/server/internal/models"
	"docforge/server/internal/store"

	"github.com/go-chi/chi/v5"
)

// handleListKeys 当前用户的 API Key。
func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r)
	var keys []models.APIKey
	s.DB.Where("user_id = ?", p.User.ID).Order("created_at DESC").Find(&keys)
	writeJSON(w, http.StatusOK, keys)
}

// handleCreateKey 创建 key，明文仅此一次返回。
func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r)
	var body struct {
		Name       string `json:"name"`
		QPSLimit   int    `json:"qps_limit"`
		MaxRecords int    `json:"max_records"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "请求体不是合法 JSON", nil)
		return
	}
	if body.Name == "" {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", "name 不能为空", nil)
		return
	}
	if body.QPSLimit <= 0 {
		body.QPSLimit = 5
	}
	if body.MaxRecords <= 0 {
		body.MaxRecords = s.Cfg.MaxRecords
	}
	secret := make([]byte, 20)
	_, _ = rand.Read(secret)
	plain := "df_" + hex.EncodeToString(secret)
	sum := sha256Sum(plain)
	ak := models.APIKey{
		ID: store.NewAkID(), UserID: p.User.ID, Name: body.Name,
		KeyHash: sum, Prefix: plain[:11], QPSLimit: body.QPSLimit,
		MaxRecords: body.MaxRecords, Enabled: true,
	}
	if err := s.DB.Create(&ak).Error; err != nil {
		s.renderErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id": ak.ID, "key": plain, "prefix": ak.Prefix, "name": ak.Name,
		"qps_limit": ak.QPSLimit, "max_records": ak.MaxRecords, "enabled": ak.Enabled,
		"created_at": ak.CreatedAt,
	})
}

func (s *Server) ownKey(w http.ResponseWriter, r *http.Request) (*models.APIKey, bool) {
	p := auth.PrincipalFrom(r)
	var ak models.APIKey
	if err := s.DB.First(&ak, "id = ? AND user_id = ?", chi.URLParam(r, "id"), p.User.ID).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "API Key 不存在", nil)
		return nil, false
	}
	return &ak, true
}

func (s *Server) handleDisableKey(w http.ResponseWriter, r *http.Request) {
	ak, ok := s.ownKey(w, r)
	if !ok {
		return
	}
	s.DB.Model(ak).Update("enabled", false)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (s *Server) handleEnableKey(w http.ResponseWriter, r *http.Request) {
	ak, ok := s.ownKey(w, r)
	if !ok {
		return
	}
	s.DB.Model(ak).Update("enabled", true)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	ak, ok := s.ownKey(w, r)
	if !ok {
		return
	}
	s.DB.Delete(ak)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}
