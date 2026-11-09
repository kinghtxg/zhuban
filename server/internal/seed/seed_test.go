package seed

import (
	"context"
	"os"
	"testing"

	"docforge/server/internal/models"
	"docforge/server/internal/render"
	"docforge/server/internal/storage"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// fakeRenderer 只实现播种会调用到的 Scan。其余方法继承 nil 接口，
// 一旦被调用立刻 panic —— 这正好能抓住「播种顺手做了别的事」。
type fakeRenderer struct{ render.Renderer }

func (fakeRenderer) Scan(context.Context, []byte) (*render.ScanResult, error) {
	return &render.ScanResult{
		Variables:   []interface{}{},
		SampleJSON:  map[string]interface{}{},
		JSONSchema:  map[string]interface{}{},
		Diagnostics: []render.Diagnostics{},
	}, nil
}

func newSeedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("打开内存库失败：%v", err)
	}
	// 内存库是「连接级」的：连接池开多路会让不同连接看到不同的库，
	// 迁移与断言就会互相看不见。
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取底层连接失败：%v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.Template{}, &models.TemplateVersion{}); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	return db
}

func loadTemplate(t *testing.T, db *gorm.DB, code string) models.Template {
	t.Helper()
	var tpl models.Template
	if err := db.First(&tpl, "code = ?", code).Error; err != nil {
		t.Fatalf("模板 %s 不存在：%v", code, err)
	}
	return tpl
}

func loadCurrentVersion(t *testing.T, db *gorm.DB, tpl models.Template) models.TemplateVersion {
	t.Helper()
	var ver models.TemplateVersion
	if err := db.First(&ver, "id = ?", tpl.CurrentVersionID).Error; err != nil {
		t.Fatalf("当前版本不存在：%v", err)
	}
	return ver
}

func countVersions(t *testing.T, db *gorm.DB, tplID string) int {
	t.Helper()
	var n int64
	if err := db.Model(&models.TemplateVersion{}).Where("template_id = ?", tplID).Count(&n).Error; err != nil {
		t.Fatalf("统计版本失败：%v", err)
	}
	return int(n)
}

// TestBuiltinSeedPublishesNewVersionWhenAssetChanges 守的是升级路径：
// 内置模板的 docx 随产品版本变化时，已有实例必须跟着更新。
//
// 3.4.9 的页码域是坏的，3.4.10 只修了模板资产。如果播种仍然「code 已存在就跳过」，
// 代码修好了而用户手上的模板还是旧的 —— 这类问题在开发机上永远发现不了，
// 因为开发机每次都是全新库。
func TestBuiltinSeedPublishesNewVersionWhenAssetChanges(t *testing.T) {
	db := newSeedDB(t)
	st := storage.New(t.TempDir())
	rd := fakeRenderer{}
	ctx := context.Background()

	v1 := builtin{Code: "t-demo", Name: "演示", DOCX: []byte("docx-v1"), SampleJSON: []byte(`{}`)}
	seedOne(ctx, db, st, rd, v1)

	tpl := loadTemplate(t, db, "t-demo")
	if got := loadCurrentVersion(t, db, tpl).Version; got != "1.0.0" {
		t.Fatalf("首播版本号应为 1.0.0，实际 %s", got)
	}

	// 资产没变：重复播种不该凭空多一个版本。
	seedOne(ctx, db, st, rd, v1)
	if n := countVersions(t, db, tpl.ID); n != 1 {
		t.Fatalf("内容未变却又发了新版本，现有 %d 个", n)
	}

	// 资产变了：要发 1.0.1，并切换当前版本。
	v2 := v1
	v2.DOCX = []byte("docx-v2")
	seedOne(ctx, db, st, rd, v2)

	tpl = loadTemplate(t, db, "t-demo")
	cur := loadCurrentVersion(t, db, tpl)
	if cur.Version != "1.0.1" {
		t.Fatalf("资产变化后应为 1.0.1，实际 %s", cur.Version)
	}
	if n := countVersions(t, db, tpl.ID); n != 2 {
		t.Fatalf("应保留旧版本、只追加一个，现有 %d 个", n)
	}

	// 磁盘上当前版本的文件必须是新内容 —— 只改数据库不改文件，
	// 界面显示新版本、渲染出来还是旧的，比不更新更难查。
	got, err := os.ReadFile(cur.FilePath)
	if err != nil {
		t.Fatalf("读当前版本文件失败：%v", err)
	}
	if string(got) != "docx-v2" {
		t.Fatalf("当前版本文件内容应为 docx-v2，实际 %q", string(got))
	}
	if cur.FileSize != int64(len("docx-v2")) {
		t.Fatalf("FileSize 未同步：%d", cur.FileSize)
	}
}

// TestBuiltinSeedKeepsTemplateWithUserVersion 守的是边界：
// 用户自己传过版本，说明这份模板已经是他的，产品不许覆盖。
func TestBuiltinSeedKeepsTemplateWithUserVersion(t *testing.T) {
	db := newSeedDB(t)
	st := storage.New(t.TempDir())
	rd := fakeRenderer{}
	ctx := context.Background()

	v1 := builtin{Code: "t-own", Name: "演示", DOCX: []byte("docx-v1"), SampleJSON: []byte(`{}`)}
	seedOne(ctx, db, st, rd, v1)

	tpl := loadTemplate(t, db, "t-own")
	if err := db.Model(&models.TemplateVersion{}).
		Where("id = ?", tpl.CurrentVersionID).
		Updates(map[string]interface{}{"uploaded_by": "alice", "file_size": 8}).Error; err != nil {
		t.Fatalf("改造当前版本失败：%v", err)
	}

	v2 := v1
	v2.DOCX = []byte("docx-v2")
	seedOne(ctx, db, st, rd, v2)

	tpl = loadTemplate(t, db, "t-own")
	cur := loadCurrentVersion(t, db, tpl)
	if cur.UploadedBy != "alice" {
		t.Fatalf("用户版本被换掉了：uploaded_by=%s", cur.UploadedBy)
	}
	if n := countVersions(t, db, tpl.ID); n != 1 {
		t.Fatalf("不该给用户模板追加版本，现有 %d 个", n)
	}
}
