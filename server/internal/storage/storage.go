// Package storage 管理 DATA_DIR 下的磁盘布局（契约 §5）：
// templates/{tpl}/{ver}/original.docx、jobs/{job}/*、thumbnails/*、tmp/*。
package storage

import (
	"os"
	"path/filepath"
)

// Store 提供规范化的文件落盘路径与写入能力。
type Store struct {
	Root string
}

// New 构造存储。
func New(dataDir string) *Store { return &Store{Root: dataDir} }

// ensureDir 创建目录。
func ensureDir(p string) error { return os.MkdirAll(p, 0o755) }

// TemplateVersionPath 返回模板版本源文件绝对路径，并建目录。
func (s *Store) TemplateVersionPath(tplID, verID string) (string, error) {
	dir := filepath.Join(s.Root, "templates", tplID, verID)
	if err := ensureDir(dir); err != nil {
		return "", err
	}
	return filepath.Join(dir, "original.docx"), nil
}

// TemplateVersionFile 读取模板版本源文件。
func (s *Store) TemplateVersionFile(tplID, verID string) (string, error) {
	return filepath.Join(s.Root, "templates", tplID, verID, "original.docx"), nil
}

// JobDir 返回任务产物目录，建目录。
func (s *Store) JobDir(jobID string) (string, error) {
	d := filepath.Join(s.Root, "jobs", jobID)
	return d, ensureDir(d)
}

// JobFile 返回任务目录下某文件路径（不建目录）。
func (s *Store) JobFile(jobID, name string) string {
	return filepath.Join(s.Root, "jobs", jobID, name)
}

// ThumbDir 返回缩略图目录，建目录。
func (s *Store) ThumbDir() (string, error) {
	d := filepath.Join(s.Root, "thumbnails")
	return d, ensureDir(d)
}

// ThumbPath 返回某模板的缩略图缓存路径。
func (s *Store) ThumbPath(tplID, verID string) (string, error) {
	d, err := s.ThumbDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, tplID+"_"+verID+".png"), nil
}

// TmpDir 返回临时目录。
func (s *Store) TmpDir() (string, error) {
	d := filepath.Join(s.Root, "tmp")
	return d, ensureDir(d)
}

// AssetDir 返回模板资源目录 volumes/templates/{tpl}/assets，建目录。
func (s *Store) AssetDir(tplID string) (string, error) {
	d := filepath.Join(s.Root, "templates", tplID, "assets")
	return d, ensureDir(d)
}

// AssetPath 返回某资源落盘路径（不建目录，由 WriteFile 建）。
func (s *Store) AssetPath(tplID, aid, ext string) string {
	return filepath.Join(s.Root, "templates", tplID, "assets", aid+ext)
}

// WriteFile 写文件。
func (s *Store) WriteFile(path string, data []byte, perm os.FileMode) error {
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	return os.WriteFile(path, data, perm)
}

// Exists 判断文件是否存在。
func Exists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// SealDir 返回签章素材目录 volumes/seal，建目录。
func (s *Store) SealDir() (string, error) {
	d := filepath.Join(s.Root, "seal")
	return d, ensureDir(d)
}

// SigPath 返回签名 PNG 落盘路径 volumes/seal/sig_<id>.png（不建目录，WriteFile 建）。
func (s *Store) SigPath(id string) string {
	return filepath.Join(s.Root, "seal", id+".png")
}

// SealPath 返回印章 PNG 落盘路径 volumes/seal/seal_<id>.png。
func (s *Store) SealPath(id string) string {
	return filepath.Join(s.Root, "seal", id+".png")
}
