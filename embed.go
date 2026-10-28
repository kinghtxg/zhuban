// Package docforge 存放需要跨模块内嵌的静态资源。
//
// Go 的 //go:embed 模式不能以 ".." 跳出当前源码目录，因此所有需要被
// server 内嵌的资源（前端构建产物、内置示例模板）都在仓库根包声明。
package docforge

import "embed"

// WebFS 是前端构建产物（web/dist）。仓库内自带一份占位 index.html，
// 保证在未执行 `npm run build` 时 `go build` 也能通过。
//
//go:embed all:web/dist
var WebFS embed.FS

// SiteFS 是官网静态站（site/）。
//
// 它同时是「可独立部署的静态站」和「未登录时的首页」——同一份文件，两种用法：
// 拷到任意静态托管（GitHub Pages / OSS / nginx）就能当官网；被这个二进制内嵌时
// 由 handleRoot 在未登录的 / 上直接吐出来。
//
// 单独放一份而不是塞进 web/dist，是因为前者是构建产物（每次 npm run build 重建），
// 官网不该被前端构建顺手删掉或覆盖。
//
//go:embed all:site
var SiteFS embed.FS

// InstallFS 是安装向导页（install/）。
//
// 与官网同理，零构建纯静态：装个软件不该要求先跑一遍 npm。
//
// 它和 site/ 的关键区别是**生命周期**：官网长期在线，安装页只在
// 「数据库还是空的时候」存在。首次启动时会被释放到数据目录，
// 装完后由服务端删掉（见 internal/api/install.go），此后 /install 一律 404。
//
//go:embed all:install
var InstallFS embed.FS

// AuditTemplateDOCX 是内置的「代码审计报告」迁移模板（code=audit-report）。
//
//go:embed samples/templates/audit-report.docx
var AuditTemplateDOCX []byte

// AuditSampleJSON 是内置审计模板的一键试用示例数据。
//
//go:embed samples/audit-report.sample.json
var AuditSampleJSON []byte

// BlankTemplateDOCX 是模板制作向导提供的「空白示例模板」。
//
//go:embed samples/blank-template.docx
var BlankTemplateDOCX []byte

// DemoTemplateDOCX 是开源版内置的功能演示模板（code=demo-report）。
//
// 与 AuditTemplateDOCX 的分工：审计模板是「像真业务一样能用」，演示模板是
// 「每个能力都摆出来给人看」——标量、默认值、日期表达式、表格行循环、整段
// 循环、嵌套循环、条件与 else、行内条件表达式、图片、二维码、富文本、
// 折线图、饼图、签章占位、页眉页脚页码、目录域，一样一处，每一处都带说明文字。
//
// 首次启动就播种它，新用户不必先做模板才能看到产品能干什么。
//
//go:embed samples/templates/demo-report.docx
var DemoTemplateDOCX []byte

// DemoSampleJSON 是演示模板的一键试用数据，与 DemoTemplateDOCX 配套。
//
//go:embed samples/demo-report.sample.json
var DemoSampleJSON []byte
