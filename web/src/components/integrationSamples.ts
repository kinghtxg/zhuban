/**
 * 接入示例生成器 —— 纯函数，无 React 依赖
 *
 * 重构背景：原 IntegrationCard.tsx 内同时含一个从未被渲染的 Modal 组件（约 60 行）
 * 和本函数；两处引用方只 import buildSamples。此处拆出纯函数，删除死组件。
 */

export function buildSamples(base: string, code: string, version: string, sampleJson: string) {
  const key = 'df_在APIKey页面创建的Key';
  const body = JSON.stringify(
    {
      template_code: code,
      version,
      data: JSON.parse(sampleJson),
      format: 'docx',
      external_ref: 'ORDER-123',
    },
    null,
    2,
  );

  const curl = `# 投递即生成：POST 一段 JSON，平台自动渲染并入文档生成中心
curl -X POST '${base}/api/v1/generate' \\
  -H 'X-API-Key: ${key}' \\
  -H 'Content-Type: application/json' \\
  -d '${body.replace(/'/g, "'\\''")}'

# 异步模式返回 job_id 后轮询状态：
# curl '${base}/api/v1/jobs/{job_id}' -H 'X-API-Key: ${key}'
# 任务终结后，返回体里的 signed_url 即可直接下载（24h 有效）`;

  const python = `import json
import time
import requests

BASE = "${base}"
HEADERS = {"X-API-Key": "${key}"}

resp = requests.post(
    f"{BASE}/api/v1/generate",
    headers=HEADERS,
    json=${body},
    timeout=60,
)
resp.raise_for_status()
job = resp.json()

if job.get("sync"):
    # 同步单份：documents[0].signed_url 即下载直链
    print(job["documents"][0]["signed_url"])
else:
    # 异步：轮询任务直到终结
    while True:
        s = requests.get(f"{BASE}{job['status_url']}", headers=HEADERS, timeout=30)
        state = s.json()
        if state["status"] in ("succeeded", "partial", "failed"):
            print(state.get("signed_url") or state.get("zip_download_url"))
            break
        time.sleep(2)

# 回调接收方（可选）：POST 到你提供的 callback_url，形状为
# {"job_id": "...", "status": "succeeded", "downloads": [{"filename": "...", "signed_url": "..."}]}
`;

  const goCode = `package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const base = "${base}"
const apiKey = "${key}"

func main() {
	body := ${body}
	buf, _ := json.Marshal(map[string]any{
		"template_code": "${code}",
		"version":       "${version}",
		"data":          body["data"],
		"format":        "docx",
		"external_ref":  "ORDER-123",
	})
	req, _ := http.NewRequest("POST", base+"/api/v1/generate", bytes.NewReader(buf))
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil { panic(err) }
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)

	var job map[string]any
	json.Unmarshal(rb, &job)
	if sync, _ := job["sync"].(bool); sync {
		docs, _ := job["documents"].([]any)
		if len(docs) > 0 {
			fmt.Println(docs[0].(map[string]any)["signed_url"])
		}
	} else {
		// 异步：轮询 status_url 到 succeeded/partial/failed
		fmt.Println("status_url:", job["status_url"], "signed_url:", job["signed_url"])
		_ = time.Second
	}
}
`;

  return { curl, python, go: goCode };
}
