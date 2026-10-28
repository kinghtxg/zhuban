package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"

	"docforge/server/internal/idgen"
	"docforge/server/internal/models"
)

// randID 生成 8 字节随机 hex。
func randID() string { return idgen.Hex(8) }

// osReadFile 读文件。
func osReadFile(p string) ([]byte, error) { return os.ReadFile(p) }

// serveFileDownload 以**流式**方式把文件发给客户端。
//
// 为什么不直接用 os.ReadFile + w.Write：单份文档通常只有几百 KB，看起来不严重，
// 但库里确实存在带几十张高清图的模板产物，而批量 zip 更是可以到上百 MB。
// 整包读进内存意味着峰值占用 ≈ 并发数 × 文件体积，几个客户端同时下就能把容器打爆；
// 而 OOM 的表现是「服务无响应」，排查时很难联想到是某次下载。
//
// http.ServeContent 做三件必要的事：
//  1. 按 32KB 块边读边写，内存占用与文件大小无关；
//  2. 支持 Range 请求（断点续传、下载器分段），大文件才有得谈；
//  3. 自动填 Content-Length 与 Last-Modified，客户端能看到进度。
//
// onOpen 在文件**成功打开之后、开始发送之前**调用（用来做「确实下载了才计数」），
// 可以是 nil。放在这里而不是调用方，是为了避免「先 stat 再计数再 open」那段
// 在文件刚被清理时会多计一次的 TOCTOU。
func serveFileDownload(w http.ResponseWriter, r *http.Request, path, filename, ctype string, onOpen func()) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	// 到这一步响应头还没写出去，所以上面任何一个 error 调用方都还来得及改成 410。
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	if onOpen != nil {
		onOpen()
	}
	http.ServeContent(w, r, filename, st.ModTime(), f)
	return nil
}

// docURLs 返回一份产物的下载链接。收敛到一处，是因为这个坑已经踩过一次：
//
//	primary — 该产物「默认」该给客户端的那份（有 docx 就是 docx，只有 pdf 才是 pdf）
//	pdfURL  — pdf 变体；未生成 pdf 时为空串
//
// format=pdf 的任务 DocxPath 为空，若各处仍把主链接固定写成
// /documents/{id}/download，客户端下到的是 410 + 一小段错误 JSON（实测 82 字节），
// 于是「PDF 输出坏了」——产物其实是好的，坏的是链接。
func docURLs(d models.Document) (primary, pdfURL string) {
	docxURL := "/api/v1/documents/" + d.ID + "/download"
	pdfCandidate := docxURL + "?format=pdf"
	if d.DocxPath == "" && d.PdfPath != "" {
		return pdfCandidate, pdfCandidate
	}
	if d.PdfPath == "" {
		return docxURL, ""
	}
	return docxURL, pdfCandidate
}

// decodeJSON 解码请求体。
func decodeJSON(r *http.Request, v interface{}) error {
	return json.NewDecoder(io.LimitReader(r.Body, 25<<20)).Decode(v)
}

// jsonUnmarshal 包装 json.Unmarshal。
func jsonUnmarshal(b []byte, v interface{}) error { return json.Unmarshal(b, v) }

// sha256Sum 返回 hex(sha256(s))。
func sha256Sum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// itoa 整数转十进制字符串。
func itoa(n int) string { return strconv.Itoa(n) }
