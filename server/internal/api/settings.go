package api

import (
	"encoding/json"
	"strconv"
	"time"

	"docforge/server/internal/models"

	"gorm.io/gorm"
)

// 默认设置值（V3 §3.6）。
var defaultSettings = map[string]string{
	"review_signatures_enabled":   "false",
	"review_seals_enabled":        "false",
	"seal_default_width_mm":       "40",
	"signature_default_width_mm": "60",
}

// getSetting 读单个设置，不存在则返回默认。
func (s *Server) getSetting(key string) string {
	var row models.Setting
	if err := s.DB.Where("key = ?", key).First(&row).Error; err != nil {
		if def, ok := defaultSettings[key]; ok {
			return def
		}
		return ""
	}
	return row.Value
}

// setSetting upsert 一个设置。
func (s *Server) setSetting(key, value string) error {
	row := models.Setting{Key: key, Value: value, UpdatedAt: time.Now()}
	return s.DB.Save(&row).Error
}

// settingsBool / settingsFloat 是类型化读取。
func (s *Server) settingsBool(key string) bool {
	v := s.getSetting(key)
	return v == "true" || v == "1"
}
func (s *Server) settingsFloat(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(s.getSetting(key), 64); err == nil {
		return v
	}
	return def
}

// allSettings 返回 V3 可在线修改的设置 map。
func (s *Server) v3Settings() map[string]interface{} {
	return map[string]interface{}{
		"review_signatures_enabled":   s.settingsBool("review_signatures_enabled"),
		"review_seals_enabled":        s.settingsBool("review_seals_enabled"),
		"seal_default_width_mm":       s.settingsFloat("seal_default_width_mm", 40),
		"signature_default_width_mm": s.settingsFloat("signature_default_width_mm", 60),
	}
}

// upsertSettings 从请求 JSON 白名单写入。
func (s *Server) upsertSettings(body []byte) error {
	var in map[string]interface{}
	if err := json.Unmarshal(body, &in); err != nil {
		return errBadRequest("请求体不是 JSON 对象")
	}
	whitelist := map[string]bool{
		"review_signatures_enabled": true, "review_seals_enabled": true,
		"seal_default_width_mm": true, "signature_default_width_mm": true,
	}
	for k, v := range in {
		if !whitelist[k] {
			continue
		}
		var sv string
		switch t := v.(type) {
		case bool:
			sv = strconv.FormatBool(t)
		case float64:
			sv = strconv.FormatFloat(t, 'f', -1, 64)
		case string:
			sv = t
		default:
			continue
		}
		if err := s.setSetting(k, sv); err != nil {
			return err
		}
	}
	return nil
}

// ensureNoRowsAffected 空函数占位，避免 unused。
var _ = gorm.ErrRecordNotFound
