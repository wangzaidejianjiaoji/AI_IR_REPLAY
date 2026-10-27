package progress

import (
	"bytes"
	"fmt"
	"html/template"
	"path/filepath"
	"sort"
	"time"

	"gvs-pr-agent/internal/instance"
)

type healthView struct {
	Date      string
	Generated string
	Live      bool
	Rows      []healthRow
	Dates     []string
	Alerts    int
}

type healthRow struct {
	Name     string
	Updated  int64
	Running  bool
	State    string
	Day      healthDay
	Previous healthDay
	Advice   []string
	Resolved []string
	Objects  []*healthObject
}

// Render under one project lock. Ledgers are committed first, so a busy renderer
// can defer to the other instance without dropping any accounting updates.
func renderHealth(dir string, now time.Time) error {
	lock, err := instance.Acquire(filepath.Join(dir, "report.lock"))
	if err != nil {
		if instance.IsBusy(err) {
			return nil
		}
		return err
	}
	defer lock.Close()
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	var ledgers []healthLedger
	dates := map[string]bool{healthDate(now): true}
	for _, path := range paths {
		ledger, err := readHealth(path)
		if err != nil {
			return err
		}
		ledgers = append(ledgers, ledger)
		for date := range ledger.Days {
			dates[date] = true
		}
	}
	allDates := make([]string, 0, len(dates))
	for date := range dates {
		allDates = append(allDates, date)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(allDates)))
	today := healthDate(now)
	// Only today's report and the last observed prior day need refreshing.
	// Older daily HTML remains archived; recreating a removed archive is safe.
	for _, date := range allDates {
		if _, err := time.Parse("2006-01-02", date); err != nil {
			return fmt.Errorf("invalid health date %q", date)
		}
		if date != today && date != healthDate(now.AddDate(0, 0, -1)) {
			if matches, _ := filepath.Glob(filepath.Join(dir, date+".html")); len(matches) != 0 {
				continue
			}
		}
		view := healthView{Date: date, Generated: now.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02 15:04:05 +08:00"), Dates: allDates}
		dateTime, _ := time.Parse("2006-01-02", date)
		previousDate := dateTime.AddDate(0, 0, -1).Format("2006-01-02")
		for _, ledger := range ledgers {
			day := ledger.Days[date]
			if day == nil {
				day = &healthDay{}
			}
			previous := ledger.Days[previousDate]
			if previous == nil {
				previous = &healthDay{}
			}
			row := healthRow{Name: ledger.Name, Day: *day, Previous: *previous, Updated: ledger.UpdatedAt.UnixMilli(), Running: ledger.Running, State: ledger.State}
			row.Advice = healthAdvice(*day, *previous)
			view.Alerts += len(row.Advice)
			// Compare stable categories, not changing evidence numbers.
			if previous.UsageMissing > 0 && day.UsageMissing == 0 && day.Requests > 0 {
				row.Resolved = append(row.Resolved, "今日已有请求且未观测到前日的 usage 缺失。")
			}
			if previous.Interrupted > 0 && day.Interrupted == 0 && day.Completed > 0 {
				row.Resolved = append(row.Resolved, "今日已有审核完成且未观测到前日的中断。")
			}
			if previous.Requests >= 10 && previous.CacheHits == 0 && day.CacheHits > 0 {
				row.Resolved = append(row.Resolved, "今日已观测到检查点缓存复用。")
			}
			for _, object := range day.Objects {
				row.Objects = append(row.Objects, object)
			}
			sort.Slice(row.Objects, func(i, j int) bool {
				if row.Objects[i].TotalTokens != row.Objects[j].TotalTokens {
					return row.Objects[i].TotalTokens > row.Objects[j].TotalTokens
				}
				return row.Objects[i].Task+row.Objects[i].Version < row.Objects[j].Task+row.Objects[j].Version
			})
			if len(row.Objects) > 20 {
				row.Objects = row.Objects[:20]
			}
			view.Rows = append(view.Rows, row)
		}
		var buf bytes.Buffer
		if err := healthTemplate.Execute(&buf, view); err != nil {
			return err
		}
		if err := writeHealth(filepath.Join(dir, date+".html"), buf.Bytes()); err != nil {
			return err
		}
		if date == today {
			view.Live = true
			buf.Reset()
			if err := healthTemplate.Execute(&buf, view); err != nil {
				return err
			}
			if err := writeHealth(filepath.Join(dir, "index.html"), buf.Bytes()); err != nil {
				return err
			}
		}
	}
	return nil
}

var healthTemplate = template.Must(template.New("health").Funcs(template.FuncMap{
	"duration": formatDuration,
}).Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
{{if .Live}}<meta http-equiv="refresh" content="15">{{end}}
<title>项目健康度 · {{.Date}}</title>
<style>
:root{font-family:system-ui,"Microsoft YaHei",sans-serif;color:#183044;background:#f1f5f8}body{max-width:1200px;margin:32px auto;padding:0 20px}h1{font-size:30px}h2{font-size:22px}h3{font-size:17px}section,.note{background:white;border:1px solid #d9e3eb;border-radius:12px;padding:20px;margin:18px 0}.muted{color:#526779;font-size:14px}.cards{display:flex;flex-wrap:wrap;gap:12px}.cards div{background:#edf4f8;border-radius:8px;padding:14px;min-width:130px}.cards strong{display:block;font-size:25px}table{border-collapse:collapse;width:100%;font-size:14px}td,th{text-align:left;padding:10px;border-bottom:1px solid #e4ebf0;overflow-wrap:anywhere}th{color:#526779}.scroll{overflow:auto}a{color:#116682;margin-right:12px}.alert{color:#934100}.ok{color:#24654c}li{margin:8px 0;line-height:1.6}nav{line-height:2}code{overflow-wrap:anywhere}footer{margin:24px 0;font-size:13px;line-height:1.8;color:#526779}
</style></head><body>
<header><p class="muted">AI_IR_REPLAY / 运行观测</p><h1>项目健康度检查</h1><p>{{.Date}} · 北京时间 · {{if .Alerts}}<span class="alert">{{.Alerts}} 项需审查{{else}}<span class="ok">暂无规则告警{{end}}</span></p><p class="muted">报告生成：{{.Generated}}。{{if .Live}}每 15 秒刷新；心跳过期不代表已确认进程死亡。{{else}}每日审查归档；当日数据在运行期间持续更新。{{end}}</p></header>
<nav><a href="index.html">实时总览</a>{{range .Dates}}<a href="{{.}}.html">{{.}}</a>{{end}}</nav>
{{range .Rows}}<section><h2>{{.Name}}</h2>
{{if $.Live}}<p class="heartbeat muted" data-updated="{{.Updated}}" data-running="{{.Running}}">最近心跳时间戳 {{.Updated}} · {{if .Running}}监控随审核进程运行{{else}}监控已停止{{end}} · 最近审核状态 {{.State}}</p>{{end}}
<div class="cards"><div>已知总 token<strong>{{.Day.TotalTokens}}</strong>输入 {{.Day.PromptTokens}} / 输出 {{.Day.CompletionTokens}}</div><div>模型请求<strong>{{.Day.Requests}}</strong>usage 缺失 {{.Day.UsageMissing}}</div><div>检查点复用<strong>{{.Day.CacheHits}}</strong>不重复计入模型请求</div><div>新增审核尝试<strong>{{.Day.Attempts}}</strong>完成 {{.Day.Completed}} / 中断 {{.Day.Interrupted}}</div><div>观测审核耗时<strong>{{duration .Day.ElapsedSeconds}}</strong>累计各尝试时间</div></div>
<h3>每日审查与优化建议</h3><ul>{{range .Advice}}<li class="alert">{{.}}</li>{{else}}<li>{{if .Day.Requests}}当前观测未触发规则；这不证明审核质量或成本最优。{{else}}尚无模型请求样本，不能判断模型效率。{{end}}</li>{{end}}</ul>
<h3>前日对比与处理记录</h3><p class="muted">前日 token {{.Previous.TotalTokens}} / 请求 {{.Previous.Requests}} / 新增尝试 {{.Previous.Attempts}} / 中断 {{.Previous.Interrupted}}。当日尚未结束，需结合工作量比较。</p><ul>{{range .Resolved}}<li class="ok">{{.}}</li>{{else}}<li>尚无足够证据标记前日问题消退。</li>{{end}}</ul><p class="muted">处理状态：建议已记录，待人工评估实施；指标改善不自动认定为优化造成。系统不会自动修改代码、模型或审核预算。</p>
<h3>用量较高的审核对象（最多 20 项）</h3><div class="scroll"><table><thead><tr><th>仓库 / 审核对象</th><th>版本</th><th>当日新增尝试</th><th>当日 token</th></tr></thead><tbody>{{range .Objects}}<tr><td>{{.Repository}} {{if .Title}}{{.Title}}{{else}}{{.Task}}{{end}}</td><td>{{.Version}}</td><td>{{.Attempts}}</td><td>{{.TotalTokens}}</td></tr>{{else}}<tr><td colspan="4">尚无审核样本</td></tr>{{end}}</tbody></table></div></section>{{end}}
<footer>数据口径：供应商响应 usage 的累计增量，不是账单；缺失 usage、尚未返回的请求和强制退出前最后一个心跳间隔可能未计入。无可靠价格时不估算金额。输入占比 &gt;90% 且总量 ≥10000、请求 ≥10 且无缓存、token ≥100000 且超过前日 50%、平均观测耗时 &gt;30 分钟是启发式提醒，不代表已证实浪费。首次启用仅采集之后的运行，不回灌旧历史。按北京时间观察日期入账，跨午夜请求计入收到用量的日期，耗时按采样增量归属。每日账本独立于最近 100 条审核历史保留；报告及账本不含提示词、源码或凭据，但包含仓库和审核标题。程序未运行期间不执行每日审查。</footer>
{{if .Live}}<script>document.querySelectorAll('.heartbeat').forEach(function(el){var updated=Number(el.dataset.updated);var stale=Date.now()-updated>60000;el.textContent='最近心跳 '+new Date(updated).toLocaleString('zh-CN',{timeZone:'Asia/Shanghai'})+'（北京时间） · '+(el.dataset.running==='true'?(stale?'心跳已过期，请检查进程和日志':'监控运行中'):'监控已停止');if(stale&&el.dataset.running==='true')el.classList.add('alert');});</script>{{end}}
</body></html>`))
