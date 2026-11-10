package config

import (
	"testing"
	"time"
)

// Go 侧「等渲染服务多久」必须是可配的。
//
// 起因是一个只在慢机器上出现的失败：Windows 便携版导出一份含 7 张截图的
// 渗透报告 PDF，要做「目录页码两遍排版」（每遍一次 LibreOffice 转换），
// 70 秒不够，任务稳定 failed，而 error 只有一句
// `Post "http://127.0.0.1:18101/render": context deadline exceeded` ——
// 看起来像渲染服务挂了，实际只是 Go 没耐心。同一个模板在生产 Linux 上
// 20 秒内就出来了，所以这种缺陷不可能在服务器上发现。
func TestRenderTimeoutsAreConfigurable(t *testing.T) {
	// 默认值必须保持不变：新增环境变量不能改变任何现有部署的行为。
	t.Setenv("RENDER_TIMEOUT_SECONDS", "")
	t.Setenv("SCAN_TIMEOUT_SECONDS", "")
	t.Setenv("PREVIEW_TIMEOUT_SECONDS", "")
	c := Load()
	if c.RenderTimeout != 70*time.Second {
		t.Fatalf("RenderTimeout 默认应为 70s，实得 %v", c.RenderTimeout)
	}
	if c.ScanTimeout != 30*time.Second {
		t.Fatalf("ScanTimeout 默认应为 30s，实得 %v", c.ScanTimeout)
	}
	if c.PreviewTimeout != 90*time.Second {
		t.Fatalf("PreviewTimeout 默认应为 90s，实得 %v", c.PreviewTimeout)
	}

	// 覆盖生效。
	t.Setenv("RENDER_TIMEOUT_SECONDS", "180")
	t.Setenv("SCAN_TIMEOUT_SECONDS", "45")
	t.Setenv("PREVIEW_TIMEOUT_SECONDS", "240")
	c = Load()
	if c.RenderTimeout != 180*time.Second {
		t.Fatalf("RenderTimeout 覆盖失败，实得 %v", c.RenderTimeout)
	}
	if c.ScanTimeout != 45*time.Second {
		t.Fatalf("ScanTimeout 覆盖失败，实得 %v", c.ScanTimeout)
	}
	if c.PreviewTimeout != 240*time.Second {
		t.Fatalf("PreviewTimeout 覆盖失败，实得 %v", c.PreviewTimeout)
	}

	// 写错不该让服务起不来，退回默认值（envInt 的既有语义）。
	t.Setenv("RENDER_TIMEOUT_SECONDS", "三分钟")
	c = Load()
	if c.RenderTimeout != 70*time.Second {
		t.Fatalf("非法值应退回 70s，实得 %v", c.RenderTimeout)
	}
}
