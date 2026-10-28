package auditx_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"docforge/server/internal/auditx"
	"docforge/server/internal/config"
	"docforge/server/internal/models"
	"docforge/server/internal/store"
)

func setup(t *testing.T) *auditx.Chain {
	dir := t.TempDir()
	cfg := &config.Config{DatabaseURL: "sqlite://" + dir + "/t.db", DataDir: dir}
	db, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return &auditx.Chain{DB: db}
}

func TestChainConcurrentWriteVerify(t *testing.T) {
	c := setup(t)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, _ = c.Write(&auditx.Entry{Action: "test.action", ObjectType: "obj", ObjectID: fmt.Sprintf("o%d", n),
				OccurredAt: time.Now()})
		}(i)
	}
	wg.Wait()
	ok, checked, _, _, _ := c.Verify()
	if !ok || checked != 50 {
		t.Fatalf("expected ok=true checked=50, got ok=%v checked=%d", ok, checked)
	}
}

func TestChainTamperDetected(t *testing.T) {
	c := setup(t)
	for i := 0; i < 5; i++ {
		_, _ = c.Write(&auditx.Entry{Action: "a", ObjectType: "o", ObjectID: fmt.Sprintf("o%d", i)})
	}
	// 篡改第 3 条 detail。
	var rows []models.AuditLog
	c.DB.Order("seq ASC").Find(&rows)
	victim := rows[2]
	c.DB.Model(&models.AuditLog{}).Where("id=?", victim.ID).Update("detail", `{"tampered":true}`)
	ok, _, brokenSeq, _, _ := c.Verify()
	if ok {
		t.Fatal("expected broken chain")
	}
	if brokenSeq != victim.Seq {
		t.Fatalf("expected broken at seq %d, got %d", victim.Seq, brokenSeq)
	}
}

func TestChainGenesis(t *testing.T) {
	c := setup(t)
	row, err := c.Write(&auditx.Entry{Action: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if row.PrevHash != auditx.GenesisPrevHash {
		t.Fatalf("genesis prev_hash wrong: %s", row.PrevHash)
	}
	if row.Seq != 1 {
		t.Fatalf("first seq should be 1, got %d", row.Seq)
	}
}

func TestBackfillMixedOldNullAndNewChained(t *testing.T) {
	c := setup(t)
	// 先写 3 条已链接的新行。
	for i := 0; i < 3; i++ {
		if _, err := c.Write(&auditx.Entry{Action: "v3", ObjectID: fmt.Sprintf("new%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	// 手工插入 2 条 V2 旧行（临时占位值），随后把 seq/self_hash 置空模拟生产 NULL 脏行。
	old := []models.AuditLog{
		{ID: "aud_old1", Seq: 100, SelfHash: "old1", Action: "v2.a", ObjectType: "template", ObjectID: "t1", CreatedAt: time.Now().Add(-time.Hour)},
		{ID: "aud_old2", Seq: 101, SelfHash: "old2", Action: "v2.b", ObjectType: "user", ObjectID: "u1", CreatedAt: time.Now().Add(-30 * time.Minute)},
	}
	for i := range old {
		if err := c.DB.Create(&old[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 模拟生产：V2 行 seq/self_hash 为 NULL（PG 唯一索引允许多个 NULL）。
	c.DB.Model(&models.AuditLog{}).Where("id IN ?", []string{"aud_old1", "aud_old2"}).
		Updates(map[string]interface{}{"seq": nil, "self_hash": nil, "prev_hash": ""})
	// 跑 backfill（不报错、不冲突）。
	if err := c.BootstrapBackfill(); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	// 旧行 seq 排在前面（1、2）。
	var rows []models.AuditLog
	c.DB.Order("created_at ASC, id ASC").Find(&rows)
	if len(rows) != 5 {
		t.Fatalf("expected 5 rows, got %d", len(rows))
	}
	if rows[0].Seq != 1 || rows[1].Seq != 2 {
		t.Fatalf("old rows should be seq 1,2; got %d,%d", rows[0].Seq, rows[1].Seq)
	}
	ok, checked, _, _, _ := c.Verify()
	if !ok || checked != 5 {
		t.Fatalf("expected ok checked=5, got ok=%v checked=%d", ok, checked)
	}
	// 幂等再跑一次，hash 不变。
	before := rows[0].SelfHash
	if err := c.BootstrapBackfill(); err != nil {
		t.Fatal(err)
	}
	var row0 models.AuditLog
	c.DB.First(&row0, "id=?", rows[0].ID)
	if row0.SelfHash != before {
		t.Fatal("idempotent backfill changed hash")
	}
}
