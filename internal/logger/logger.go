// Package logger 提供与 GVS 日志体系一致的结构化日志。
// 基于标准库 log/slog，支持三种格式：
//   - json: 结构化 JSON（容器部署由 docker/systemd 收集）
//   - text: slog 默认文本格式
//   - gvs:  与 GVS gvlogger 一致的 "2006/01/02 15:04:05 [I] 消息 key=val" 前缀风格
//
// 输出目标支持 stdout / file / both（文件路径与 GVS 共享 /opt/lz/log/ 目录）。
package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// New 根据配置创建 slog.Logger。
// output: stdout/file/both；file/both 时写入 filePath（目录不存在时自动创建）。
func New(level, format, output, filePath string) *slog.Logger {
	lv := parseLevel(level)
	w := buildWriter(output, filePath)

	opts := &slog.HandlerOptions{Level: lv}
	var handler slog.Handler
	switch strings.ToLower(format) {
	case "text":
		handler = slog.NewTextHandler(w, opts)
	case "gvs":
		handler = newGVSHandler(w, lv)
	default:
		handler = slog.NewJSONHandler(w, opts)
	}
	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// buildWriter 按输出目标构建 writer；文件打开失败时回退为 stdout。
func buildWriter(output, filePath string) io.Writer {
	switch strings.ToLower(output) {
	case "file", "both":
		if filePath == "" {
			return os.Stdout
		}
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "创建日志目录失败，回退 stdout:", err)
			return os.Stdout
		}
		f, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, "打开日志文件失败，回退 stdout:", err)
			return os.Stdout
		}
		if strings.ToLower(output) == "both" {
			return io.MultiWriter(os.Stdout, f)
		}
		return f
	default:
		return os.Stdout
	}
}

// gvsHandler 实现 GVS gvlogger 风格的日志格式：
// "2006/01/02 15:04:05 [I] 消息 key=val key2=val2"
// 级别缩写与 gvlogger.LogLevel.String() 一致：D/I/W/E。
type gvsHandler struct {
	mu    *sync.Mutex
	w     io.Writer
	level slog.Level
	attrs []slog.Attr
}

func newGVSHandler(w io.Writer, level slog.Level) *gvsHandler {
	return &gvsHandler{mu: &sync.Mutex{}, w: w, level: level}
}

func (h *gvsHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *gvsHandler) Handle(_ context.Context, r slog.Record) error {
	var sb strings.Builder
	sb.WriteString(r.Time.Format("2006/01/02 15:04:05"))
	sb.WriteString(" [")
	sb.WriteString(levelAbbr(r.Level))
	sb.WriteString("] ")
	sb.WriteString(r.Message)

	appendAttr := func(a slog.Attr) {
		sb.WriteString(" ")
		sb.WriteString(a.Key)
		sb.WriteString("=")
		sb.WriteString(fmt.Sprint(a.Value.Any()))
	}
	for _, a := range h.attrs {
		appendAttr(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		appendAttr(a)
		return true
	})
	sb.WriteString("\n")

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, sb.String())
	return err
}

func (h *gvsHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	return &gvsHandler{mu: h.mu, w: h.w, level: h.level, attrs: merged}
}

func (h *gvsHandler) WithGroup(name string) slog.Handler {
	// gvlogger 无分组概念，退化为前缀属性
	return h.WithAttrs([]slog.Attr{slog.String("group", name)})
}

// levelAbbr 与 gvlogger.LogLevel.String() 的缩写保持一致。
func levelAbbr(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "E"
	case l >= slog.LevelWarn:
		return "W"
	case l >= slog.LevelInfo:
		return "I"
	default:
		return "D"
	}
}

// 编译期确认 gvsHandler 实现 slog.Handler
var _ slog.Handler = (*gvsHandler)(nil)
