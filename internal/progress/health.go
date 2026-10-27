package progress

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gvs-pr-agent/internal/instance"
)

// Each instance owns its ledger. Daily counters survive the bounded review history.
type healthLedger struct {
	Schema    int                   `json:"schema"`
	Name      string                `json:"name"`
	UpdatedAt time.Time             `json:"updated_at"`
	Running   bool                  `json:"running"`
	State     string                `json:"state"`
	Last      Snapshot              `json:"last"`
	Days      map[string]*healthDay `json:"days"`
}

type healthDay struct {
	Attempts         int                      `json:"attempts"`
	Completed        int                      `json:"completed"`
	Interrupted      int                      `json:"interrupted"`
	PromptTokens     int                      `json:"prompt_tokens"`
	CompletionTokens int                      `json:"completion_tokens"`
	TotalTokens      int                      `json:"total_tokens"`
	Requests         int                      `json:"requests"`
	UsageMissing     int                      `json:"usage_missing"`
	CacheHits        int                      `json:"cache_hits"`
	ElapsedSeconds   float64                  `json:"elapsed_seconds"`
	Objects          map[string]*healthObject `json:"objects"`
}

type healthObject struct {
	Task        string `json:"task"`
	Version     string `json:"version"`
	Repository  string `json:"repository"`
	Title       string `json:"title"`
	Attempts    int    `json:"attempts"`
	TotalTokens int    `json:"total_tokens"`
}

func healthDate(now time.Time) string {
	return now.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02")
}

func healthDirectory(statusPath string) string {
	dir := filepath.Dir(statusPath)
	if filepath.Base(dir) == "runtime" {
		dir = filepath.Dir(dir)
	}
	return filepath.Join(dir, "health")
}

func readHealth(path string) (healthLedger, error) {
	var ledger healthLedger
	raw, err := os.ReadFile(path)
	if err != nil {
		return ledger, err
	}
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return ledger, fmt.Errorf("健康账本损坏 %s: %w", path, err)
	}
	if ledger.Schema != 1 || ledger.Days == nil {
		return ledger, fmt.Errorf("健康账本格式不支持: %s", path)
	}
	for _, day := range ledger.Days {
		if day == nil || day.Objects == nil {
			return ledger, fmt.Errorf("健康日记录损坏: %s", path)
		}
		for _, object := range day.Objects {
			if object == nil {
				return ledger, fmt.Errorf("健康对象记录损坏: %s", path)
			}
		}
	}
	return ledger, nil
}

// recordHealth only counts increases within an attempt, including its final snapshot.
// A new PID/start time is a new attempt; persisted high water marks make restarts safe.
func recordHealth(dir, name string, s Snapshot, now time.Time, running bool) error {
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return fmt.Errorf("invalid health instance %q", name)
	}
	lock, err := instance.Acquire(filepath.Join(dir, name+".lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	path := filepath.Join(dir, name+".json")
	ledger, err := readHealth(path)
	if os.IsNotExist(err) {
		ledger = healthLedger{Schema: 1, Name: name, Days: map[string]*healthDay{}}
	} else if err != nil {
		return err
	}
	date := healthDate(now)
	day := ledger.Days[date]
	if day == nil {
		day = &healthDay{Objects: map[string]*healthObject{}}
		ledger.Days[date] = day
	}
	if s.Task != "" && !s.StartedAt.IsZero() {
		previous := ledger.Last
		fresh := previous.PID != s.PID || !previous.StartedAt.Equal(s.StartedAt) || previous.Task != s.Task || previous.Version != s.Version
		if fresh {
			previous = Snapshot{}
			day.Attempts++
		}
		key := s.Task + "\x00" + s.Version
		object := day.Objects[key]
		if object == nil {
			object = &healthObject{}
			day.Objects[key] = object
		}
		object.Task, object.Version, object.Repository, object.Title = s.Task, s.Version, s.Repository, s.Title
		if fresh {
			object.Attempts++
		}
		delta := func(current, old int) int { return max(0, current-old) }
		day.PromptTokens += delta(s.PromptTokens, previous.PromptTokens)
		day.CompletionTokens += delta(s.CompletionTokens, previous.CompletionTokens)
		tokens := delta(s.TotalTokens, previous.TotalTokens)
		day.TotalTokens += tokens
		object.TotalTokens += tokens
		day.Requests += delta(s.Requests, previous.Requests)
		day.UsageMissing += delta(s.UsageMissing, previous.UsageMissing)
		day.CacheHits += delta(s.CacheHits, previous.CacheHits)
		day.ElapsedSeconds += max(0, s.ElapsedSeconds-previous.ElapsedSeconds)
		if previous.FinishedAt.IsZero() && !s.FinishedAt.IsZero() {
			if s.State == "completed" {
				day.Completed++
			} else {
				day.Interrupted++
			}
		}
		// Retain high water marks if an older snapshot is ever replayed.
		s.PromptTokens = max(s.PromptTokens, previous.PromptTokens)
		s.CompletionTokens = max(s.CompletionTokens, previous.CompletionTokens)
		s.TotalTokens = max(s.TotalTokens, previous.TotalTokens)
		s.Requests = max(s.Requests, previous.Requests)
		s.UsageMissing = max(s.UsageMissing, previous.UsageMissing)
		s.CacheHits = max(s.CacheHits, previous.CacheHits)
		s.ElapsedSeconds = max(s.ElapsedSeconds, previous.ElapsedSeconds)
		if s.FinishedAt.IsZero() && !previous.FinishedAt.IsZero() {
			s.FinishedAt, s.State = previous.FinishedAt, previous.State
		}
	}
	// An idle startup must not erase the preceding attempt's deduplication cursor.
	if s.Task != "" {
		ledger.Last = s
	}
	ledger.UpdatedAt, ledger.Running, ledger.State = now, running, s.State
	raw, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	if err := writeHealth(path, raw); err != nil {
		return err
	}
	return renderHealth(dir, now)
}

func writeHealth(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".health-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func healthAdvice(day, previous healthDay) []string {
	var advice []string
	if day.UsageMissing > 0 {
		advice = append(advice, fmt.Sprintf("%d 次请求缺失 usage：核对供应商账单与网络错误；当前 token 可能低估。", day.UsageMissing))
	}
	if day.Interrupted > 0 {
		advice = append(advice, fmt.Sprintf("%d 次审核中断：检查失败阶段、超时及限流，优先复用检查点再重试。", day.Interrupted))
	}
	repeated := 0
	for _, object := range day.Objects {
		repeated += max(0, object.Attempts-1)
	}
	if repeated > 0 {
		advice = append(advice, fmt.Sprintf("同一对象版本出现 %d 次额外尝试：检查重复调度和恢复路径；重试可能合理，不等同于浪费。", repeated))
	}
	if day.Requests >= 10 && day.CacheHits == 0 {
		advice = append(advice, fmt.Sprintf("%d 次请求、0 次检查点缓存命中：检查是否启用及可复用；首次审核无命中属正常。", day.Requests))
	}
	if day.TotalTokens >= 10000 && day.PromptTokens > 9*day.CompletionTokens {
		advice = append(advice, fmt.Sprintf("输入占比偏高（%d 输入 / %d 输出）：抽查重复上下文与分包，缩减前确认不损害覆盖。", day.PromptTokens, day.CompletionTokens))
	}
	if previous.TotalTokens > 0 && day.TotalTokens >= 100000 && float64(day.TotalTokens) > float64(previous.TotalTokens)*1.5 {
		advice = append(advice, fmt.Sprintf("token 高于前日 50%%（%d → %d）：先比较审核工作量、MR 大小和重试，不直接归因于效率退化。", previous.TotalTokens, day.TotalTokens))
	}
	if day.Completed+day.Interrupted > 0 && day.ElapsedSeconds/float64(day.Completed+day.Interrupted) > 1800 {
		advice = append(advice, "平均观测审核耗时超过 30 分钟：检查大 MR、排队与重试；跨日/进行中审核会影响此比值。")
	}
	return advice
}
