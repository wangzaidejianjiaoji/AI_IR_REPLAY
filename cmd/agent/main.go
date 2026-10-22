// gvs-pr-agent 入口：加载配置、组装审查服务、启动定时调度器与管理 HTTP 服务。
//
// 用法示例：
//
//	gvs-pr-agent -config configs/config.yaml            # 常驻运行（每分钟轮询）
//	gvs-pr-agent -config configs/config.yaml -once      # 只跑一轮后退出
//	gvs-pr-agent -config configs/config.yaml -dry-run   # 只审查不发布评论
//	gvs-pr-agent -config configs/config.yaml -repo <仓库全路径> -mr <编号> # 手动指定单个 Codeup MR
//	gvs-pr-agent -rule my-rules.yaml ...                # CLI 指定规则文档（四层链最高优先级）
//	gvs-pr-agent -diff my.patch -dry-run                # 审查本地 diff 文件（不依赖 GitHub）
package main

import (
	"context"
	"crypto/subtle"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gvs-pr-agent/internal/config"
	"gvs-pr-agent/internal/instance"
	"gvs-pr-agent/internal/lifecycle"
	"gvs-pr-agent/internal/logger"
	"gvs-pr-agent/internal/progress"
	"gvs-pr-agent/internal/review"
	"gvs-pr-agent/internal/scheduler"
)

func main() {
	var (
		configPath    = flag.String("config", defaultConfigPath(), "配置文件路径")
		dryRun        = flag.Bool("dry-run", false, "只审查不发布评论")
		once          = flag.Bool("once", false, "只执行一轮审查后退出")
		diffFile      = flag.String("diff", "", "审查本地 diff 文件（调试用，不访问 GitHub）")
		rulePath      = flag.String("rule", "", "规则文档路径（四层优先级链的最高层，覆盖配置文件）")
		repository    = flag.String("repo", "", "精准审查的 Codeup 仓库（必须已配置，与 -mr 配对）")
		mrID          = flag.Int("mr", 0, "精准审查的 Codeup MR 正整数 ID（与 -repo 配对，单次执行）")
		checkpointDir = flag.String("checkpoint-dir", "", "覆盖 review.checkpoint_dir，复用 MR 审查检查点")
		action        = flag.String("action", "", "实例管理：start/stop/restart/status，不执行配置扫描")
		instanceName  = flag.String("instance", "all", "管理实例：all/gvs/gvs-infer")
	)
	flag.Parse()
	if *action != "" {
		var invalid string
		flag.Visit(func(f *flag.Flag) {
			if f.Name != "action" && f.Name != "instance" {
				invalid = f.Name
			}
		})
		if invalid != "" || flag.NArg() != 0 {
			fmt.Fprintln(os.Stderr, "-action 仅允许配合 -instance 使用；指定 MR 请沿用 -repo/-mr")
			os.Exit(1)
		}
		executable, err := os.Executable()
		if err == nil {
			err = lifecycle.Run(filepath.Dir(filepath.Dir(executable)), *action, *instanceName, os.Stdout)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "instance" {
			fmt.Fprintln(os.Stderr, "-instance 必须与 -action 配合使用")
			os.Exit(1)
		}
	})
	var repoSet, mrSet bool
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "repo" {
			repoSet = true
		}
		if f.Name == "mr" {
			mrSet = true
		}
	})
	if err := validateFocusedFlags(*repository, *mrID, *diffFile, repoSet, mrSet); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载配置失败:", err)
		os.Exit(1)
	}
	if *dryRun {
		cfg.Review.DryRun = true
	}
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "checkpoint-dir" {
			cfg.Review.CheckpointDir = *checkpointDir
		}
	})
	if err := cfg.ValidateRuntime(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	log := logger.New(cfg.Logging.Level, cfg.Logging.Format, cfg.Logging.Output, cfg.Logging.FilePath)
	log.Info("gvs-pr-agent 启动", "config", *configPath,
		"dry_run", cfg.Review.DryRun, "pr_comment", cfg.Notification.PRComment)

	// 本地 diff 模式不需要 GitHub Token
	if *diffFile == "" {
		if err := cfg.Validate(); err != nil {
			log.Error("配置校验失败", "error", err.Error())
			os.Exit(1)
		}
	} else if cfg.LLM.APIKey == "" {
		log.Error("缺少 KIMI_API_KEY")
		os.Exit(1)
	}
	// Hold OS locks for every mutable state resource until process exit.
	paths := []string{}
	if cfg.Codeup.Enabled {
		paths = append(paths, cfg.Codeup.StatePath+".lock")
	}
	if cfg.Understanding.Enabled {
		paths = append(paths, cfg.Understanding.DocumentPath+".lock")
	}
	if len(paths) == 0 && *diffFile == "" {
		paths = append(paths, "./data/github-agent.lock")
	}
	for _, p := range paths {
		lock, err := instance.Acquire(p)
		if err != nil {
			log.Error("实例锁失败", "error", err.Error())
			os.Exit(1)
		}
		defer lock.Close()
	}

	svc, err := review.NewService(cfg, *rulePath, log)
	if err != nil {
		log.Error("初始化审查服务失败", "error", err.Error())
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	statusName := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(*configPath), "config-"), filepath.Ext(*configPath))
	tracker := progress.New(filepath.Join("data", "runtime", statusName+".status.json"), log)
	ctx = progress.WithTracker(ctx, tracker)
	defer tracker.Run(ctx, 15*time.Second)()

	// 规则文档热更新（rules.hot_reload 开启时生效）
	svc.StartRuleHotReload(ctx.Done())

	// 模式 1：本地 diff 审查（调试）
	if *diffFile != "" {
		data, err := os.ReadFile(*diffFile)
		if err != nil {
			log.Error("读取 diff 文件失败", "error", err.Error())
			os.Exit(1)
		}
		report, err := svc.ReviewLocalDiff(ctx, string(data), "本地 diff 审查: "+*diffFile)
		if err != nil {
			log.Error("本地 diff 审查失败", "error", err.Error())
			os.Exit(1)
		}
		fmt.Println(report)
		return
	}

	// 手动选择只在本次进程生效，不改变定时扫描配置或保存为长期筛选条件。
	if *repository != "" {
		if err := svc.ReviewCodeupMR(ctx, *repository, *mrID); err != nil {
			log.Error("指定 MR 审查失败", "repo", *repository, "mr", *mrID, "error", err.Error())
			os.Exit(1)
		}
		log.Info("指定 MR 审查完成，退出", "repo", *repository, "mr", *mrID)
		return
	}

	// 模式 2：只跑一轮
	if *once {
		svc.RunOnce(ctx)
		log.Info("单轮审查完成，退出")
		return
	}

	// 模式 3：常驻运行（定时轮询 + 管理端点）
	sched := scheduler.New(cfg.Scheduler.IntervalDuration(), cfg.Scheduler.JitterDuration(), log)
	startAdminServer(cfg.Server.Addr, cfg.Server.AdminToken, sched, log)
	sched.Run(ctx, svc.RunOnce)
}

func validateFocusedFlags(repository string, localID int, diffFile string, repoSet, mrSet bool) error {
	if repoSet != mrSet {
		return fmt.Errorf("-repo 与 -mr 必须成对指定，MR ID 必须为正整数")
	}
	if repository == "" && localID == 0 {
		return nil
	}
	if strings.TrimSpace(repository) == "" || repository != strings.TrimSpace(repository) || localID <= 0 {
		return fmt.Errorf("-repo 与 -mr 必须成对指定，仓库不能为空且 MR ID 必须为正整数")
	}
	if diffFile != "" {
		return fmt.Errorf("-repo/-mr 不能与 -diff 同时使用")
	}
	return nil
}

// startAdminServer 启动管理 HTTP 服务：GET /health 健康检查、POST /trigger 手动触发。
func startAdminServer(addr, token string, sched *scheduler.Scheduler, log interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
}) {
	mux := adminHandler(sched, token)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info("管理服务启动", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("管理服务异常退出", "error", err.Error())
		}
	}()
}

func adminHandler(sched *scheduler.Scheduler, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("POST /trigger", func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			http.Error(w, "manual trigger disabled: admin token not configured", http.StatusServiceUnavailable)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, "Bearer ")), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sched.Trigger()
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintln(w, `{"status":"triggered"}`)
	})

	return mux
}

// defaultConfigPath 默认配置路径：环境变量 AGENT_CONFIG_PATH 优先，否则 ./configs/config.yaml。
func defaultConfigPath() string {
	if p := os.Getenv("AGENT_CONFIG_PATH"); p != "" {
		return p
	}
	return "./configs/config.yaml"
}
