package sigauth

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"docforge/server/internal/config"
	"docforge/server/internal/idgen"
	"docforge/server/internal/models"
	"docforge/server/internal/store"
	"docforge/server/internal/storage"
)

func setup(t *testing.T) (*Resolver, *storage.Store, string) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, DatabaseURL: "sqlite://" + filepath.Join(dir, "t.db")}
	db, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return &Resolver{DB: db}, storage.New(dir), dir
}

func mkSig(t *testing.T, r *Resolver, st *storage.Store, owner string) *models.Signature {
	sig := models.Signature{ID: "sig_abc", Name: "张三签", OwnerID: owner, Source: "upload",
		Status: "approved", FilePath: filepath.Join(t.TempDir(), "sig_abc.png")}
	_ = os.WriteFile(sig.FilePath, []byte("\x89PNG\r\n\x1a\nFAKE"), 0o644)
	if err := r.DB.Create(&sig).Error; err != nil {
		t.Fatal(err)
	}
	return &sig
}

func grantFor(t *testing.T, r *Resolver, sigID, grantee string, maxUses int, from, until *time.Time) *models.Grant {
	g := models.Grant{ID: "grt_" + idgen.Hex(4), ResourceType: "signature", ResourceID: sigID,
		GranteeType: "user", GranteeID: grantee, GranteeName: "member", MaxUses: maxUses,
		ValidFrom: from, ValidUntil: until}
	if err := r.DB.Create(&g).Error; err != nil {
		t.Fatal(err)
	}
	return &g
}

func TestOwnerBypass(t *testing.T) {
	r, st, _ := setup(t)
	sig := mkSig(t, r, st, "u_owner")
	out, err := r.Inject([]byte(`{"`+sig.ID+`":""}`), Options{GranteeType: "user", GranteeID: "u_owner"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("data:image/png;base64,")) {
		t.Fatalf("expected data:URI injected, got %s", out)
	}
	// owner 不扣次数。
	var g models.Grant
	if r.DB.First(&g, "resource_id=?", sig.ID).Error == nil {
		t.Fatal("owner should not consume grant")
	}
}

func TestNoGrantDenied(t *testing.T) {
	r, st, _ := setup(t)
	sig := mkSig(t, r, st, "u_owner")
	_, err := r.Inject([]byte(`{"`+sig.ID+`":""}`), Options{GranteeType: "user", GranteeID: "u_member"})
	if d, ok := err.(*ErrDenied); !ok || d.Reason != "unauthorized" {
		t.Fatalf("expected unauthorized ErrDenied, got %v", err)
	}
}

func TestSingleUseGrant(t *testing.T) {
	r, st, _ := setup(t)
	sig := mkSig(t, r, st, "u_owner")
	g := grantFor(t, r, sig.ID, "u_member", 1, nil, nil)
	// 第一次成功。
	if _, err := r.Inject([]byte(`{"`+sig.ID+`":""}`), Options{GranteeType: "user", GranteeID: "u_member"}); err != nil {
		t.Fatal(err)
	}
	// 第二次应 403 exhausted。
	_, err := r.Inject([]byte(`{"`+sig.ID+`":""}`), Options{GranteeType: "user", GranteeID: "u_member"})
	if d, ok := err.(*ErrDenied); !ok || d.Reason != "exhausted" {
		t.Fatalf("expected exhausted, got %v", err)
	}
	var after models.Grant
	r.DB.First(&after, "id=?", g.ID)
	if after.UsedCount != 1 {
		t.Fatalf("expected used_count=1, got %d", after.UsedCount)
	}
}

func TestWindowNotYetAndExpired(t *testing.T) {
	r, st, _ := setup(t)
	sig := mkSig(t, r, st, "u_owner")
	future := time.Now().Add(time.Hour)
	grantFor(t, r, sig.ID, "u_member", 5, &future, nil)
	_, err := r.Inject([]byte(`{"`+sig.ID+`":""}`), Options{GranteeType: "user", GranteeID: "u_member"})
	if d, ok := err.(*ErrDenied); !ok || d.Reason != "not_yet" {
		t.Fatalf("expected not_yet, got %v", err)
	}
	past := time.Now().Add(-time.Hour)
	grantFor(t, r, sig.ID, "u_member2", 5, nil, &past)
	_, err = r.Inject([]byte(`{"`+sig.ID+`":""}`), Options{GranteeType: "user", GranteeID: "u_member2"})
	if d, ok := err.(*ErrDenied); !ok || d.Reason != "expired" {
		t.Fatalf("expected expired, got %v", err)
	}
}

func TestPendingStatus422(t *testing.T) {
	r, st, _ := setup(t)
	sig := mkSig(t, r, st, "u_owner")
	r.DB.Model(sig).Update("status", "pending")
	_, err := r.Inject([]byte(`{"`+sig.ID+`":""}`), Options{GranteeType: "user", GranteeID: "u_owner"})
	if _, ok := err.(*ErrStatus); !ok {
		t.Fatalf("expected ErrStatus, got %v", err)
	}
}

func TestDryRunNoConsume(t *testing.T) {
	r, st, _ := setup(t)
	sig := mkSig(t, r, st, "u_owner")
	g := grantFor(t, r, sig.ID, "u_member", 1, nil, nil)
	if _, err := r.Inject([]byte(`{"`+sig.ID+`":""}`),
		Options{GranteeType: "user", GranteeID: "u_member", DryRun: true}); err != nil {
		t.Fatal(err)
	}
	var after models.Grant
	r.DB.First(&after, "id=?", g.ID)
	if after.UsedCount != 0 {
		t.Fatalf("DryRun should not consume, used_count=%d", after.UsedCount)
	}
}

func TestConcurrentSingleUse(t *testing.T) {
	r, st, _ := setup(t)
	sig := mkSig(t, r, st, "u_owner")
	grantFor(t, r, sig.ID, "u_member", 1, nil, nil)
	var wg sync.WaitGroup
	okCount := 0
	var mu sync.Mutex
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Inject([]byte(`{"`+sig.ID+`":""}`), Options{GranteeType: "user", GranteeID: "u_member"})
			if err == nil {
				mu.Lock()
				okCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if okCount != 1 {
		t.Fatalf("expected exactly 1 success under concurrency, got %d", okCount)
	}
}

func TestUserDataPriority(t *testing.T) {
	r, st, _ := setup(t)
	sig := mkSig(t, r, st, "u_owner")
	// 用户已提供值则不覆盖、不扣。
	out, err := r.Inject([]byte(`{"`+sig.ID+`":"user-provided"}`),
		Options{GranteeType: "user", GranteeID: "u_member"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("user-provided")) {
		t.Fatalf("user data should be preserved: %s", out)
	}
}

// ---------------------------------------------------------------------------
// V3 补充：编辑器新插入的 sig_/seal_ 占位变量发现链路
// ---------------------------------------------------------------------------

func TestScanPlaceholdersFromModel(t *testing.T) {
	model := []byte(`{
		"blocks": [
			{"id":"p0","type":"paragraph","text":"hi"},
			{"id":"new-1","type":"placeholder","kind":"image","name":"sig_abc"},
			{"id":"new-2","type":"placeholder","kind":"image","name":"seal_xyz"},
			{"id":"new-3","type":"placeholder","kind":"qrcode","name":"sig_qr"},
			{"id":"t0","type":"table","rows":[
				{"id":"r0","loop_row":false,"cells":[{"blocks":[
					{"id":"c0p0","type":"placeholder","kind":"image","name":"sig_cell"}]}]},
				{"id":"r1","loop_row":true,"cells":[{"blocks":[
					{"id":"c1p0","type":"placeholder","kind":"image","name":"sig_loop"}]}]}
			]}
		]
	}`)
	names := ScanPlaceholders(model)
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	if !got["sig_abc"] || !got["seal_xyz"] || !got["sig_cell"] {
		t.Fatalf("expected sig_abc/seal_xyz/sig_cell, got %v", names)
	}
	// qrcode kind 不算图片占位；loop_row 单元格内的跳过
	if got["sig_qr"] || got["sig_loop"] {
		t.Fatalf("qrcode/loop_row placeholders should be skipped: %v", names)
	}
}

func TestScanPlaceholdersByIDField(t *testing.T) {
	// 不带 sig_/seal_ 前缀但带 signature_id 字段的块也要被发现。
	model := []byte(`{"blocks":[
		{"id":"new-1","type":"placeholder","kind":"image","name":"myimg","signature_id":"sig_abc"}
	]}`)
	names := ScanPlaceholders(model)
	if len(names) != 1 || names[0] != "myimg" {
		t.Fatalf("expected [myimg], got %v", names)
	}
}

func TestInjectPlaceholdersThenInjectAuthorized(t *testing.T) {
	r, st, _ := setup(t)
	sig := mkSig(t, r, st, "u_owner")
	grantFor(t, r, sig.ID, "u_member", 1, nil, nil)
	// data 里原本没有 sig_abc（编辑器新插入占位）。
	data := []byte(`{"other":"x"}`)
	// 先把占位名以空值补进 data。
	data = InjectPlaceholders(data, []string{sig.ID})
	// DryRun：member 有授权 → 不扣次且注入 data:URI。
	out, err := r.Inject(data, Options{GranteeType: "user", GranteeID: "u_member", DryRun: true})
	if err != nil {
		t.Fatalf("authorized member should pass DryRun: %v", err)
	}
	if !bytes.Contains(out, []byte("data:image/png;base64,")) {
		t.Fatalf("expected data:URI injected, got %s", out)
	}
	var g models.Grant
	r.DB.First(&g, "resource_id=?", sig.ID)
	if g.UsedCount != 0 {
		t.Fatalf("DryRun should not consume, used=%d", g.UsedCount)
	}
}

func TestInjectPlaceholdersUnauthorizedDenied(t *testing.T) {
	r, st, _ := setup(t)
	sig := mkSig(t, r, st, "u_owner")
	// member 无授权。
	data := InjectPlaceholders(nil, []string{sig.ID})
	_, err := r.Inject(data, Options{GranteeType: "user", GranteeID: "u_member"})
	if _, ok := err.(*ErrDenied); !ok {
		t.Fatalf("expected ErrDenied, got %v", err)
	}
}

func TestInjectPlaceholdersConsumesGrantOnce(t *testing.T) {
	r, st, _ := setup(t)
	sig := mkSig(t, r, st, "u_owner")
	g := grantFor(t, r, sig.ID, "u_member", 1, nil, nil)
	data := InjectPlaceholders(nil, []string{sig.ID})
	if _, err := r.Inject(data, Options{GranteeType: "user", GranteeID: "u_member"}); err != nil {
		t.Fatal(err)
	}
	var after models.Grant
	r.DB.First(&after, "id=?", g.ID)
	if after.UsedCount != 1 {
		t.Fatalf("expected used_count=1, got %d", after.UsedCount)
	}
}

func TestSampleStringStillInjected(t *testing.T) {
	r, st, _ := setup(t)
	sig := mkSig(t, r, st, "u_owner")
	grantFor(t, r, sig.ID, "u_member", 1, nil, nil)
	// sample_json 自动把 sig_abc 填成普通字符串（不是图片对象）。
	data := []byte(`{"` + sig.ID + `":"示例签名"}`)
	// InjectPlaceholders 应删掉这个字符串、置空，交给 Inject 注入。
	data = InjectPlaceholders(data, []string{sig.ID})
	out, err := r.Inject(data, Options{GranteeType: "user", GranteeID: "u_member"})
	if err != nil {
		t.Fatalf("sample string should still go through injection: %v", err)
	}
	if !bytes.Contains(out, []byte("data:image/png;base64,")) {
		t.Fatalf("expected data:URI, got %s", out)
	}
}

func TestVarsSigNames(t *testing.T) {
	vars := []byte(`[{"name":"title","kind":"scalar"},{"name":"sig_abc"},{"name":"seal_xyz"},"plain"]`)
	names := VarsSigNames(vars)
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	if !got["sig_abc"] || !got["seal_xyz"] {
		t.Fatalf("expected sig_abc/seal_xyz, got %v", names)
	}
	if got["title"] || got["plain"] {
		t.Fatalf("non-sig vars should be excluded: %v", names)
	}
}

// TestDryRunPreciseReason 验证 DryRun 预检在过期/未生效/耗尽时返回精确 reason（不再硬编码 unauthorized）。
func TestDryRunPreciseReason(t *testing.T) {
	cases := []struct {
		name       string
		maxUses    int
		usedCount  int
		from, until *time.Time
		wantReason string
	}{
		{"expired", 5, 0, nil, ptrTime(time.Now().Add(-time.Hour)), "expired"},
		{"not_yet", 5, 0, ptrTime(time.Now().Add(time.Hour)), nil, "not_yet"},
		{"exhausted", 1, 1, nil, nil, "exhausted"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, st, _ := setup(t)
			sig := mkSig(t, r, st, "u_owner")
			g := grantFor(t, r, sig.ID, "u_member", c.maxUses, c.from, c.until)
			if c.usedCount > 0 {
				r.DB.Model(g).Update("used_count", c.usedCount)
			}
			_, err := r.Inject([]byte(`{"`+sig.ID+`":""}`),
				Options{GranteeType: "user", GranteeID: "u_member", DryRun: true})
			d, ok := err.(*ErrDenied)
			if !ok {
				t.Fatalf("expected ErrDenied, got %v", err)
			}
			if d.Reason != c.wantReason {
				t.Fatalf("case %s: expected reason %s, got %s", c.name, c.wantReason, d.Reason)
			}
			// DryRun 不扣次数。
			var after models.Grant
			r.DB.First(&after, "id=?", g.ID)
			if after.UsedCount != c.usedCount {
				t.Fatalf("DryRun should not consume, used_count changed: %d -> %d", c.usedCount, after.UsedCount)
			}
		})
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// ---- 值传资源 ID：模板写 {{ sig_审核人 }}，data 里传 {"sig_审核人": "sig_xxx"} ----

func mkSigWithID(t *testing.T, r *Resolver, id, owner string) *models.Signature {
	sig := models.Signature{ID: id, Name: "按值注入", OwnerID: owner, Source: "upload",
		Status: "approved", FilePath: filepath.Join(t.TempDir(), id+".png")}
	if err := os.WriteFile(sig.FilePath, []byte("\x89PNG\r\n\x1a\nFAKE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.DB.Create(&sig).Error; err != nil {
		t.Fatal(err)
	}
	return &sig
}

func TestInjectPlaceholders_KeepsResourceIDString(t *testing.T) {
	// 资源 ID 和「示例字符串」都长得像普通字符串，只有 ID 要留。
	// 删错了 → 按值注入失效（回退成拿变量名当 ID，必然查不到）；
	// 留错了 → 示例字符串被当成 ID 去查库，试运行直接 422。
	data := []byte(`{"sig_审核人":"sig_a1b2c3d4e5f60718","seal_公章":"示例信息技术有限公司"}`)
	out := InjectPlaceholders(data, []string{"sig_审核人", "seal_公章"})

	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["sig_审核人"] != "sig_a1b2c3d4e5f60718" {
		t.Fatalf("资源 ID 被清掉了：%v", m["sig_审核人"])
	}
	if m["seal_公章"] != "" {
		t.Fatalf("示例字符串应被清空以触发注入，实际 %v", m["seal_公章"])
	}
}

func TestInject_ResolvesByValue(t *testing.T) {
	r, _, _ := setup(t)
	id := "sig_0f1e2d3c4b5a6978"
	mkSigWithID(t, r, id, "u_owner")

	out, err := r.Inject([]byte(`{"sig_审核人":"`+id+`"}`),
		Options{GranteeType: "user", GranteeID: "u_owner"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("data:image/png;base64,")) {
		t.Fatalf("按值传 ID 没有被解析成图片：%s", out)
	}
}

func TestInject_FallsBackToKeyAsID(t *testing.T) {
	// 编辑器插入占位符时变量名就是资源 ID，值为空。存量模板全靠这条路径，
	// 改按值解析时不能把它弄坏。
	r, _, _ := setup(t)
	sig := mkSigWithID(t, r, "sig_abc123", "u_owner")
	_ = sig

	out, err := r.Inject([]byte(`{"sig_abc123":""}`),
		Options{GranteeType: "user", GranteeID: "u_owner"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("data:image/png;base64,")) {
		t.Fatalf("变量名即资源 ID 的旧路径失效：%s", out)
	}
}

func TestInject_PlainStringIsLeftAlone(t *testing.T) {
	// 既不是 ID 也不是空：用户想把这段文字直接印上去，尊重它，别去查库。
	r, _, _ := setup(t)
	out, err := r.Inject([]byte(`{"sig_审核人":"张三"}`), Options{GranteeType: "user", GranteeID: "u_x"})
	if err != nil {
		t.Fatalf("普通字符串不该触发查库报错：%v", err)
	}
	if !bytes.Contains(out, []byte("张三")) {
		t.Fatalf("普通字符串被改写了：%s", out)
	}
}
