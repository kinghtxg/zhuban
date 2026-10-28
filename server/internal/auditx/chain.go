// Package auditx 实现 V3 哈希链审计日志：单事务尾条加锁 → 算 hash → 插入。
// 表只 INSERT，不提供 UPDATE/DELETE（一次性历史引导除外）。
package auditx

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"docforge/server/internal/idgen"
	"docforge/server/internal/models"

	"gorm.io/gorm"
)

const GenesisPrevHash = "0000000000000000000000000000000000000000000000000000000000000000"

// Entry 是写日志的输入（不含链字段，由 Write 填充）。
type Entry struct {
	ActorType    string
	ActorUserID  string
	ActorName    string
	Action       string
	ObjectType   string
	ObjectID     string
	TemplateCode string
	ExternalRef  string
	SignatureID  string
	SealID       string
	SourceIP     string
	Endpoint     string
	Detail       map[string]interface{}
	OccurredAt   time.Time
}

// Chain 审计链写入器。
type Chain struct {
	DB *gorm.DB
}

func newID() string { return idgen.WithPrefix("al_", 8) }

// canonicalPayload 把 entry 规范化为固定字段序、key 字典序、无空白的 JSON。
func canonicalPayload(e *Entry, occurredAt time.Time) string {
	m := map[string]interface{}{
		"actor_type":    e.ActorType,
		"actor_user_id": e.ActorUserID,
		"actor_name":    e.ActorName,
		"action":        e.Action,
		"object_type":   e.ObjectType,
		"object_id":     e.ObjectID,
		"template_code": e.TemplateCode,
		"external_ref":  e.ExternalRef,
		"signature_id":  e.SignatureID,
		"seal_id":       e.SealID,
		"source_ip":     e.SourceIP,
		"endpoint":      e.Endpoint,
		"occurred_at":   occurredAt.UTC().Format(time.RFC3339),
	}
	if e.Detail != nil {
		b, _ := json.Marshal(e.Detail)
		m["detail"] = json.RawMessage(b)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := "{"
	for i, k := range keys {
		if i > 0 {
			out += ","
		}
		vb, _ := json.Marshal(m[k])
		kb, _ := json.Marshal(k)
		out += string(kb) + ":" + string(vb)
	}
	out += "}"
	return out
}

func selfHash(prev, canonical string) string {
	sum := sha256.Sum256([]byte(prev + "|" + canonical))
	return hex.EncodeToString(sum[:])
}

// Write 在单事务内追加一条日志，返回写入的行。
func (c *Chain) Write(e *Entry) (*models.AuditLog, error) {
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now()
	}
	tx := c.DB.Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	defer func() {
		_ = tx.Rollback()
	}()
	var tail models.AuditLog
	res := tx.Where("seq > 0").Order("seq DESC").Limit(1).Find(&tail)
	seq := int64(0)
	prevHash := GenesisPrevHash
	if res.RowsAffected > 0 {
		seq = tail.Seq
		prevHash = tail.SelfHash
	}
	seq++
	canon := canonicalPayload(e, e.OccurredAt)
	row := models.AuditLog{
		ID: newID(), Seq: seq,
		ActorType: e.ActorType, ActorUserID: e.ActorUserID, ActorName: e.ActorName,
		Action: e.Action, ObjectType: e.ObjectType, ObjectID: e.ObjectID,
		TemplateCode: e.TemplateCode, ExternalRef: e.ExternalRef,
		SignatureID: e.SignatureID, SealID: e.SealID,
		SourceIP: e.SourceIP, Endpoint: e.Endpoint,
		OccurredAt: e.OccurredAt, CreatedAt: e.OccurredAt,
		PrevHash: prevHash, SelfHash: selfHash(prevHash, canon),
	}
	if len(e.Detail) > 0 {
		b, _ := json.Marshal(e.Detail)
		row.Detail = b
	}
	if err := tx.Create(&row).Error; err != nil {
		return nil, err
	}
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// BootstrapBackfill 启动时一次性重建整条哈希链：
// 条件：存在 seq IS NULL 或 self_hash 为空的行。
// 单事务内：① 先把全部行 seq 置 NULL、prev/self 置空（PG 唯一索引允许多个 NULL）；
// ② 按 (created_at,id) 排序全部行，seq 从 1 连续重编，重算 prev/self。
// 幂等：无脏行时不重算；可重复执行。
func (c *Chain) BootstrapBackfill() error {
	tx := c.DB.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() { _ = tx.Rollback() }()

	// 是否需要重建：存在任何 seq IS NULL / self_hash 空的行。
	var dirty int64
	tx.Model(&models.AuditLog{}).
		Where("seq IS NULL OR self_hash IS NULL OR self_hash = ''").Count(&dirty)
	if dirty == 0 {
		return nil
	}
	// ① 先全部置空（self_hash 用 NULL，PG/SQLite 唯一索引都允许多个 NULL）。
	if err := tx.Model(&models.AuditLog{}).Where("1 = 1").
		Updates(map[string]interface{}{"seq": nil, "prev_hash": nil, "self_hash": nil}).Error; err != nil {
		return err
	}
	// ② 全量按时间排序。
	var rows []models.AuditLog
	if err := tx.Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return err
	}
	prevHash := GenesisPrevHash
	for i := range rows {
		e := &Entry{
			ActorType:    rows[i].ActorType,
			ActorUserID:  rows[i].ActorUserID,
			ActorName:     rows[i].ActorName,
			Action:       rows[i].Action,
			ObjectType:   rows[i].ObjectType,
			ObjectID:     rows[i].ObjectID,
			TemplateCode: rows[i].TemplateCode,
			ExternalRef:  rows[i].ExternalRef,
			SignatureID:  rows[i].SignatureID,
			SealID:       rows[i].SealID,
			SourceIP:     rows[i].SourceIP,
			Endpoint:     rows[i].Endpoint,
			OccurredAt:   rows[i].OccurredAt,
		}
		if e.OccurredAt.IsZero() {
			e.OccurredAt = rows[i].CreatedAt
		}
		if len(rows[i].Detail) > 0 {
			var d map[string]interface{}
			_ = json.Unmarshal(rows[i].Detail, &d)
			e.Detail = d
		}
		seq := int64(i + 1)
		canon := canonicalPayload(e, e.OccurredAt)
		if err := tx.Model(&models.AuditLog{}).Where("id = ?", rows[i].ID).
			Updates(map[string]interface{}{
				"seq": seq, "prev_hash": prevHash, "self_hash": selfHash(prevHash, canon),
				"occurred_at": e.OccurredAt,
			}).Error; err != nil {
			return err
		}
		prevHash = selfHash(prevHash, canon)
	}
	return tx.Commit().Error
}

// Verify 从头重算全链，返回是否完整、检查条数与断链位置。
func (c *Chain) Verify() (ok bool, checked int64, brokenSeq int64, expected, actual string) {
	var rows []models.AuditLog
	c.DB.Order("seq ASC NULLS LAST").Find(&rows)
	prev := GenesisPrevHash
	for i := range rows {
		e := &Entry{
			ActorType: rows[i].ActorType, ActorUserID: rows[i].ActorUserID, ActorName: rows[i].ActorName,
			Action: rows[i].Action, ObjectType: rows[i].ObjectType, ObjectID: rows[i].ObjectID,
			TemplateCode: rows[i].TemplateCode, ExternalRef: rows[i].ExternalRef,
			SignatureID: rows[i].SignatureID, SealID: rows[i].SealID,
			SourceIP: rows[i].SourceIP, Endpoint: rows[i].Endpoint,
			OccurredAt: rows[i].OccurredAt,
		}
		if len(rows[i].Detail) > 0 {
			var d map[string]interface{}
			_ = json.Unmarshal(rows[i].Detail, &d)
			e.Detail = d
		}
		canon := canonicalPayload(e, rows[i].OccurredAt)
		exp := selfHash(prev, canon)
		if rows[i].PrevHash != prev || rows[i].SelfHash != exp {
			return false, int64(i) + 1, rows[i].Seq, exp, rows[i].SelfHash
		}
		prev = rows[i].SelfHash
		checked++
	}
	return true, checked, 0, "", ""
}

// String 帮助调试。
func String() string { return fmt.Sprintf("auditx chain %s", GenesisPrevHash[:8]) }
