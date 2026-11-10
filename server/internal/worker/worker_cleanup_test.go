package worker

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"docforge/server/internal/models"
)

// seedTpl 造一个模板 + 版本 + 磁盘上的版本目录与缩略图，返回目录与缩略图路径。
func seedTpl(t *testing.T, w *Worker, cfgTplID, tplID, verID string, deletedAt time.Time) (string, string) {
	t.Helper()
	tpl := models.Template{ID: tplID, Code: cfgTplID, Name: "测试模板", CurrentVersionID: verID}
	if err := w.DB.Create(&tpl).Error; err != nil {
		t.Fatal(err)
	}
	if !deletedAt.IsZero() {
		// gorm.DeletedAt 的软删只会写当前时间，历史时刻得直接改
		if err := w.DB.Unscoped().Model(&tpl).Update("deleted_at", deletedAt).Error; err != nil {
			t.Fatal(err)
		}
	}

	dir := filepath.Join(w.Store.Root, "templates", tplID, verID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	docx := filepath.Join(dir, "original.docx")
	if err := os.WriteFile(docx, []byte("docx"), 0o644); err != nil {
		t.Fatal(err)
	}
	thumbDir := filepath.Join(w.Store.Root, "thumbnails")
	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	thumb := filepath.Join(thumbDir, tplID+"_"+verID+".png")
	if err := os.WriteFile(thumb, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(w.Store.Root, "templates", tplID), thumb
}

// 保留期内的软删不能删文件：这期间 POST /documents/{id}/regenerate 还要读回
// 版本文件重新渲染，删了就把重新生成打成 500。
func TestCleanupDeletedTemplatesKeepsWithinRetention(t *testing.T) {
	w, _ := setupWorker(t)
	dir, thumb := seedTpl(t, w, "tpl-keep", "tpl_keep", "ver_1", time.Now().AddDate(0, 0, -1))

	w.cleanupDeletedTemplates()

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("保留期内的模板目录被删了: %v", err)
	}
	if _, err := os.Stat(thumb); err != nil {
		t.Fatalf("保留期内的缩略图被删了: %v", err)
	}
}

// 过了保留期才真正回收，且缩略图要一起走——只删目录不删缩略图等于没清干净。
func TestCleanupDeletedTemplatesReclaimsExpired(t *testing.T) {
	w, _ := setupWorker(t)
	dir, thumb := seedTpl(t, w, "tpl-gone", "tpl_gone", "ver_1", time.Now().AddDate(0, 0, -8))

	w.cleanupDeletedTemplates()

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("过期模板目录没被回收: %v", err)
	}
	if _, err := os.Stat(thumb); !os.IsNotExist(err) {
		t.Fatalf("过期模板缩略图没被回收: %v", err)
	}
	// 数据库软删行必须留着：历史 job / document 还引用着 template_id
	var n int64
	w.DB.Unscoped().Model(&models.Template{}).Where("id = ?", "tpl_gone").Count(&n)
	if n != 1 {
		t.Fatalf("软删行被连带删了，审计链会断: n=%d", n)
	}
}

// 活模板即使磁盘上有目录也不能碰。
func TestCleanupDeletedTemplatesSkipsAlive(t *testing.T) {
	w, _ := setupWorker(t)
	dir, thumb := seedTpl(t, w, "tpl-alive", "tpl_alive", "ver_1", time.Time{})

	w.cleanupDeletedTemplates()

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("活模板目录被删了: %v", err)
	}
	if _, err := os.Stat(thumb); err != nil {
		t.Fatalf("活模板缩略图被删了: %v", err)
	}
}

// DATA_DIR 不是绝对路径时 filepath.Join 会退化成相对路径，RemoveAll 就变成
// 删进程当前目录。这条防的是「配置写错把工作区删了」。
func TestCleanupDeletedTemplatesRejectsRelativeRoot(t *testing.T) {
	w, _ := setupWorker(t)
	// 目录先按真实的绝对 Root 造好，再把 Root 换成相对路径——
	// 顺序反了的话 seedTpl 自己就会写到相对路径下，测试等于没测。
	realDir, thumb := seedTpl(t, w, "tpl-rel", "tpl_rel", "ver_1", time.Now().AddDate(0, 0, -8))
	w.Store.Root = filepath.Join("..", "..", "internal")

	w.cleanupDeletedTemplates()

	if _, err := os.Stat(realDir); err != nil {
		t.Fatalf("相对 Root 时不该动任何文件: %v", err)
	}
	if _, err := os.Stat(thumb); err != nil {
		t.Fatalf("相对 Root 时不该动缩略图: %v", err)
	}
}

// 重复跑要幂等：第二轮已经没有目录和缩略图，不能报错也不能 panic。
func TestCleanupDeletedTemplatesIdempotent(t *testing.T) {
	w, _ := setupWorker(t)
	seedTpl(t, w, "tpl-twice", "tpl_twice", "ver_1", time.Now().AddDate(0, 0, -8))

	w.cleanupDeletedTemplates()
	w.cleanupDeletedTemplates()
}
