// Package packager 实现智能文件打包（确定性工程层）：
// 将关联文件按模块归并为同一审查单元（审查包），每个包作为独立审查任务，
// 上下文隔离、天然支持并发；并按包内路径决定参与审查的 Agent 角色（动态决策入口）。
package packager

import (
	"fmt"
	"sort"
	"strings"

	"gvs-pr-agent/internal/diff"
	"gvs-pr-agent/internal/filter"
	"gvs-pr-agent/internal/textbudget"
)

// PackBounded retains complete file patches; it never truncates review evidence.
func PackBounded(files []*diff.FileDiff, maxFiles, maxBytes int) ([]Package, error) {
	var out []Package
	for _, pkg := range Pack(files, maxFiles) {
		part := Package{Name: pkg.Name}
		bytes := 0
		flush := func() {
			if len(part.Files) > 0 {
				part.Name = fmt.Sprintf("%s-%d", pkg.Name, len(out)+1)
				out = append(out, part)
				part = Package{Name: pkg.Name}
				bytes = 0
			}
		}
		for _, file := range pkg.Files {
			if maxBytes > 0 && len(file.Patch) > maxBytes {
				return nil, fmt.Errorf("审查覆盖不完整：文件 %s 的 diff 为 %d 字节，超过 max_diff_chars=%d；拒绝截断审核", file.Path, len(file.Patch), maxBytes)
			}
			if maxBytes > 0 && bytes+len(file.Patch) > maxBytes {
				flush()
			}
			part.Files = append(part.Files, file)
			bytes += len(file.Patch)
		}
		flush()
	}
	return out, nil
}

// Package 一个审查包：一组关联文件构成的独立审查单元。
type Package struct {
	Name  string           // 包名（模块名，如 "cam"、"dnn"）
	Files []*diff.FileDiff // 包内文件
}

// ChangedLines 包内变更总行数（用于日志与统计）。
func (p *Package) ChangedLines() int {
	return diff.TotalChangedLines(p.Files)
}

// DiffText 拼接包内所有文件的 diff，超过 maxChars 时截断。
func (p *Package) DiffText(maxChars int) string {
	var sb strings.Builder
	for _, f := range p.Files {
		sb.WriteString(f.Patch)
	}
	s := sb.String()
	if maxChars > 0 && len(s) > maxChars {
		s = textbudget.Limit(s, maxChars) + "\n... (diff 超长已截断)"
	}
	return s
}

// moduleGroups GVS 项目的模块归并规则：路径命中同一组的文件打包在一起审查。
// 例如 cam/cam.go 与 cam/virtualcam.go、cam/camfactory/** 同属"摄像头模块"包。
var moduleGroups = []struct {
	name     string
	patterns []string
}{
	{"摄像头模块", []string{"**/cam/**", "**/onvifcam/**"}},
	{"视频流模块", []string{"**/stream/**", "**/gvrtmp/**", "**/fastcap/**", "**/datastorage/**"}},
	{"跟踪模块", []string{"**/track/**", "**/trackerengine/**", "**/trajectory/**", "**/smooth/**"}},
	{"HTTP API 模块", []string{"**/gvhttp/**"}},
	{"AI 推理模块", []string{"**/dnn/**", "**/grdetect/**", "**/skysegmenter/**", "**/heatmap_filter/**"}},
	{"数据流引擎", []string{"**/gvflow/**", "**/gvscheduler/**"}},
	{"状态机模块", []string{"**/statemachine/**"}},
	{"通信模块", []string{"**/msg/**", "**/mqttc/**", "**/communication/**", "**/pidrpc/**"}},
	{"配置与工具", []string{"**/gvutils/**", "**/appdir/**", "**/gvlogger/**", "**/gvtypes/**", "**/gvtables/**"}},
	{"校准模块", []string{"**/cali/**", "**/rtk/**", "**/zoomcontrol/**"}},
}

// Pack 将变更文件归并为审查包：
//  1. 按模块归并规则匹配（关联文件进同一包）
//  2. 未命中任何模块的文件按顶层目录归并
//  3. 单包超过 maxPackageSize 个文件时拆分为多个子包
func Pack(files []*diff.FileDiff, maxPackageSize int) []Package {
	if maxPackageSize <= 0 {
		maxPackageSize = 5
	}

	grouped := map[string][]*diff.FileDiff{}
	var order []string
	add := func(name string, f *diff.FileDiff) {
		if _, ok := grouped[name]; !ok {
			order = append(order, name)
		}
		grouped[name] = append(grouped[name], f)
	}

	for _, f := range files {
		name := moduleFor(f.Path)
		add(name, f)
	}

	var out []Package
	for _, name := range order {
		group := grouped[name]
		// 包内按路径排序，保证输出稳定
		sort.Slice(group, func(i, j int) bool { return group[i].Path < group[j].Path })
		// 超过 maxPackageSize 拆分子包
		for i := 0; i < len(group); i += maxPackageSize {
			end := i + maxPackageSize
			if end > len(group) {
				end = len(group)
			}
			pkgName := name
			if len(group) > maxPackageSize {
				pkgName = fmt.Sprintf("%s-%d", name, i/maxPackageSize+1)
			}
			out = append(out, Package{Name: pkgName, Files: group[i:end]})
		}
	}
	return out
}

// moduleFor 返回文件路径所属的模块名；未命中模块规则时回退为顶层目录名。
func moduleFor(path string) string {
	for _, g := range moduleGroups {
		for _, p := range g.patterns {
			if filter.MatchPath(path, p) {
				return g.name
			}
		}
	}
	// 回退：按顶层目录归并（如 "qatool/ipscan.go" -> "qatool"）
	if i := strings.Index(path, "/"); i > 0 {
		return path[:i]
	}
	return "根目录"
}

// agentAssignments 包路径 -> 追加参与的 Agent 角色（doc §7.3 步骤 2）。
// 全局 Agent（security/architect/standard/concurrency/resource）始终参与所有包。
var agentAssignments = []struct {
	patterns []string
	roles    []string
}{
	{[]string{
		"**/gvflow/**", "**/gvscheduler/**",
		"**/stream/**", "**/cam/**", "**/gvrtmp/**", "**/fastcap/**", "**/datastorage/**", "**/onvifcam/**",
		"**/dnn/**", "**/grdetect/**", "**/skysegmenter/**", "**/heatmap_filter/**",
		"**/track/**", "**/trackerengine/**", "**/trajectory/**", "**/smooth/**",
		"**/msg/**", "**/communication/**", "**/mqttc/**", "**/pidrpc/**",
		"src/models/**", "src/core/**", "src/service/**", "streamer/**", "test_model/**",
	}, []string{"dataflow"}},
	{[]string{"src/models/**", "src/core/**", "streamer/**"}, []string{"ai", "ai_tool", "performance"}},
	{[]string{"src/service/**"}, []string{"security", "performance", "ui"}},
	{[]string{"test_model/**"}, []string{"test", "ai", "performance"}},
	{[]string{"**/*_test.cpp", "**/*_test.cc", "**/*_test.cxx", "**/tests/**"}, []string{"test"}},
	{[]string{"**/gvhttp/**"}, []string{"security", "ui"}},
	{[]string{"**/stream/**", "**/cam/**", "**/gvrtmp/**", "**/datastorage/**"}, []string{"performance"}},
	{[]string{"**/dnn/**", "**/grdetect/**", "**/skysegmenter/**"}, []string{"ai", "ai_tool", "performance"}},
	{[]string{"**/*_test.go"}, []string{"test"}},
	{[]string{"**/gvflow/**", "**/gvscheduler/**"}, []string{"architect", "performance"}},
	{[]string{"**/appdir/**", "**/gvutils/**"}, []string{"ui"}},
	{[]string{"**/trajectory/**", "**/trackerengine/**", "**/track/**"}, []string{"ai", "ai_tool"}},
	{[]string{"**/statemachine/**"}, []string{"ui", "architect"}},
	{[]string{"**/mqttc/**", "**/msg/**", "**/onvifcam/**", "**/communication/**"}, []string{"security"}},
}

// globalRoles 所有包都参与的全局 Agent 角色。
var globalRoles = []string{"security", "architect", "standard", "concurrency", "resource"}

// AgentsFor 返回该包应参与审查的 Agent 角色集合：
// 全局角色 + 按包内文件路径命中的专项角色。返回空 map 时表示全部参与。
func AgentsFor(pkg Package) map[string]bool {
	roles := map[string]bool{}
	for _, r := range globalRoles {
		roles[r] = true
	}
	for _, f := range pkg.Files {
		for _, a := range agentAssignments {
			for _, p := range a.patterns {
				if filter.MatchPath(f.Path, p) {
					for _, r := range a.roles {
						roles[r] = true
					}
				}
			}
		}
	}
	return roles
}
