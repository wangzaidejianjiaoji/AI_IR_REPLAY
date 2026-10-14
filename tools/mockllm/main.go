// mockllm 是本地 Mock LLM 服务：模拟 kimi /chat/completions 接口，
// 按请求中的 Agent 角色返回预设审查问题，用于无 API Key 的全链路冒烟测试。
// 用法: go run ./tools/mockllm   （监听 127.0.0.1:19090）
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// issuesFor 按 system prompt 中的角色名返回预设问题（行号均在 sample.patch 的 hunk 范围内）。
func issuesFor(body string) string {
	switch {
	case strings.Contains(body, "安全专家"):
		return `[
  {"severity":"critical","file":"gvhttp/handler.go","line":16,"problem":"ffprobe 命令注入","detail":"cameraID 直接拼接进 sh -c 命令，攻击者可通过 camera_id 参数注入任意命令","suggestion":"使用 exec.Command 传参数列表，禁止 sh -c 字符串拼接","code":"cmd := exec.Command(\"ffprobe\", rtspURL)"},
  {"severity":"high","file":"gvhttp/handler.go","line":16,"problem":"硬编码摄像头凭据","detail":"RTSP URL 中硬编码了 admin:admin123 凭据","suggestion":"凭据从配置或环境变量读取，避免入库泄露","code":""}
]`
	case strings.Contains(body, "性能专家"):
		return `[
  {"severity":"high","file":"gvhttp/handler.go","line":22,"problem":"goroutine 泄漏","detail":"go func 内 for 死循环无退出条件，每次请求都会泄漏一个 goroutine","suggestion":"监听 ctx.Done() 并增加轮询间隔","code":"for {\n  select {\n  case <-ctx.Done():\n    return\n  case <-ticker.C:\n    checkCamera(cameraID)\n  }\n}"},
  {"severity":"medium","file":"dnn/yolov5/detector.go","line":25,"problem":"gocv Mat 未释放","detail":"BlobFromImage 返回的 blob 和 Forward 返回的 prob 未 Close，cgo 内存泄漏","suggestion":"添加 defer blob.Close() 和 defer prob.Close()","code":"defer blob.Close()"}
]`
	case strings.Contains(body, "AI 专家"):
		return `[
  {"severity":"medium","file":"dnn/yolov5/detector.go","line":24,"problem":"图像预处理未归一化","detail":"BlobFromImage 缩放系数为 1.0，未归一化到 [0,1]，推理结果会偏差","suggestion":"缩放系数改为 1.0/255.0","code":"gocv.BlobFromImage(img, 1.0/255.0, image.Pt(640,640), ...)"},
  {"severity":"low","file":"weights/model_config.json","line":3,"problem":"幻觉测试：文件不在 diff 中","detail":"该问题用于验证评论定位器过滤能力","suggestion":"应被定位器丢弃","code":""}
]`
	case strings.Contains(body, "UI 设计专家"):
		return `[
  {"severity":"medium","file":"gvhttp/handler.go","line":18,"problem":"错误响应格式不统一","detail":"c.String(500, err.Error()) 直接返回裸错误字符串，未使用统一 Response 包装","suggestion":"统一使用 {code, message, data} JSON 结构返回","code":"c.JSON(500, Response{Code:500, Message:err.Error()})"}
]`
	case strings.Contains(body, "AI 工具使用专家"):
		return `[
  {"severity":"high","file":"dnn/yolov5/detector.go","line":24,"problem":"预处理缺少归一化步骤","detail":"输入未归一化到 [0,1] 区间就送入模型推理，与 AI 专家发现的问题相同（用于验证跨 Agent 去重）","suggestion":"BlobFromImage 第二个参数使用 1.0/255.0 完成归一化","code":""}
]`
	case strings.Contains(body, "代码规范专家"):
		return `[
  {"severity":"low","file":"gvhttp/handler.go","line":18,"problem":"错误处理返回裸字符串","detail":"err.Error() 直接透传给客户端，泄漏内部实现细节","suggestion":"记录日志后返回通用错误信息","code":""}
]`
	default:
		// 架构师 / 测试专家：无问题
		return `[]`
	}
}

func main() {
	http.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
		content := issuesFor(string(body))

		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": content}},
			},
			"usage": map[string]int{"prompt_tokens": 1200, "completion_tokens": 180, "total_tokens": 1380},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Println("encode response:", err)
		}
	})

	addr := "127.0.0.1:19090"
	fmt.Println("mock llm listening on", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
