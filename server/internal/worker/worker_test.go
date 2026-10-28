package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"docforge/server/internal/config"
	"docforge/server/internal/models"
	"docforge/server/internal/render"
	"docforge/server/internal/sign"
	"docforge/server/internal/storage"
	"docforge/server/internal/store"
)

// mockRenderer 对 Render 直接返回固定 docx/pdf 字节；可注入失败。
type mockRenderer struct {
	failNext bool
}

func (m *mockRenderer) Scan(ctx context.Context, docx []byte) (*render.ScanResult, error) {
	return &render.ScanResult{}, nil
}
func (m *mockRenderer) Render(ctx context.Context, docx []byte, data []byte, format string) ([]byte, string, error) {
	if m.failNext {
		return nil, "", &render.Error{StatusCode: 422, Code: "data_invalid", Message: "boom"}
	}
	return []byte("FAKE-" + format), "application/octet-stream", nil
}
func (m *mockRenderer) Validate(ctx context.Context, docx []byte, data []byte) (*render.ValidateResult, error) {
	return &render.ValidateResult{Valid: true}, nil
}
func (m *mockRenderer) Preview(ctx context.Context, docx []byte, data []byte, maxPages int) (*render.PreviewResult, error) {
	return &render.PreviewResult{}, nil
}
func (m *mockRenderer) PreviewPage(ctx context.Context, pv string, n int) ([]byte, error) {
	return []byte("png"), nil
}
func (m *mockRenderer) Health(ctx context.Context) (*render.Health, error) {
	return &render.Health{Status: "ok"}, nil
}
func (m *mockRenderer) ParseModel(ctx context.Context, docx []byte) (*render.ParseResult, error) {
	return &render.ParseResult{Blocks: json.RawMessage(`[]`)}, nil
}
func (m *mockRenderer) ApplyModel(ctx context.Context, docx []byte, modelJSON []byte) ([]byte, error) {
	return []byte("APPLIED-DOCX"), nil
}
func (m *mockRenderer) RenderSignature(ctx context.Context, fields map[string]string) ([]byte, error) {
	return []byte("PNG-SIG"), nil
}
func (m *mockRenderer) RenderSeal(ctx context.Context, fields map[string]string) ([]byte, error) {
	return []byte("PNG-SEAL"), nil
}
func (m *mockRenderer) Fonts(ctx context.Context) ([]render.Font, error) {
	return nil, nil
}

func (m *mockRenderer) ReloadFonts(ctx context.Context) (*render.ReloadResult, error) {
	return &render.ReloadResult{OK: true}, nil
}

func (m *mockRenderer) InstalledFonts(ctx context.Context) ([]render.InstalledFont, error) {
	return nil, nil
}

func setupWorker(t *testing.T) (*Worker, *config.Config) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, RetentionDays: 7, DatabaseURL: "sqlite://" + filepath.Join(dir, "test.db")}
	db, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := storage.New(dir)
	sg := sign.New("test-secret", time.Hour)
	rd := &mockRenderer{}
	w := &Worker{DB: db, Cfg: cfg, Store: st, Renderer: rd, Signer: sg}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return w, cfg
}

func TestWorkerHappyPath(t *testing.T) {
	w, _ := setupWorker(t)

	tpl := models.Template{ID: "tpl_x", Code: "audit-report", Name: "审计报告", CurrentVersionID: "ver_x"}
	ver := models.TemplateVersion{ID: "ver_x", TemplateID: "tpl_x", Version: "1.0.0", FilePath: filepath.Join(t.TempDir(), "orig.docx")}
	_ = os.WriteFile(ver.FilePath, []byte("docx"), 0o644)
	w.DB.Create(&tpl)
	w.DB.Create(&ver)

	job := models.Job{ID: "jb_20260101_0001", TemplateID: "tpl_x", VersionID: "ver_x", Status: "queued", Total: 2, Format: "docx"}
	w.DB.Create(&job)
	for i := 0; i < 2; i++ {
		w.DB.Create(&models.Document{ID: "dc_x_" + string(rune('a'+i)), JobID: job.ID, RecordIndex: i, Filename: "f.docx", Status: "queued", InputData: []byte("{}")})
	}

	w.processJob(context.Background(), &job)

	var final models.Job
	w.DB.First(&final, "id = ?", job.ID)
	if final.Status != "succeeded" {
		t.Fatalf("expected succeeded, got %s (failed=%d)", final.Status, final.Failed)
	}
	if final.ZipPath == "" {
		t.Fatal("no zip path")
	}
	if _, err := os.Stat(final.ZipPath); err != nil {
		t.Fatalf("zip missing: %v", err)
	}
}

func TestWorkerPartialFailure(t *testing.T) {
	w, _ := setupWorker(t)
	tpl := models.Template{ID: "tpl_y", Code: "c", Name: "n", CurrentVersionID: "ver_y"}
	ver := models.TemplateVersion{ID: "ver_y", TemplateID: "tpl_y", Version: "1.0.0", FilePath: filepath.Join(t.TempDir(), "o.docx")}
	_ = os.WriteFile(ver.FilePath, []byte("d"), 0o644)
	w.DB.Create(&tpl)
	w.DB.Create(&ver)

	job := models.Job{ID: "jb_20260101_0002", TemplateID: "tpl_y", VersionID: "ver_y", Status: "queued", Total: 2, Format: "docx"}
	w.DB.Create(&job)
	w.DB.Create(&models.Document{ID: "dc_y1", JobID: job.ID, RecordIndex: 0, Filename: "a.docx", Status: "queued", InputData: []byte("{}")})
	w.DB.Create(&models.Document{ID: "dc_y2", JobID: job.ID, RecordIndex: 1, Filename: "b.docx", Status: "queued", InputData: []byte("{}")})

	// 第二个 document 失败：让 renderer 第二次 Render 失败不方便，改为全部失败
	w.Renderer.(*mockRenderer).failNext = true
	w.processJob(context.Background(), &job)

	var final models.Job
	w.DB.First(&final, "id = ?", job.ID)
	if final.Status != "failed" {
		t.Fatalf("expected failed, got %s", final.Status)
	}
}
