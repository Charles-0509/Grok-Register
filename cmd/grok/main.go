package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/grok-free-register/grok-reg/internal/clearance"
	"github.com/grok-free-register/grok-reg/internal/config"
	"github.com/grok-free-register/grok-reg/internal/cpa"
	"github.com/grok-free-register/grok-reg/internal/daemon"
	"github.com/grok-free-register/grok-reg/internal/home"
	"github.com/grok-free-register/grok-reg/internal/logx"
	"github.com/grok-free-register/grok-reg/internal/pipeline"
	"github.com/grok-free-register/grok-reg/internal/reoauth"
	"github.com/grok-free-register/grok-reg/internal/state"
)

func runCmd(bin string, args ...string) error {
	cmd := exec.Command(bin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func main() {
	args := os.Args[1:]
	if daemon.IsWorker() {
		if err := runWorker(args); err != nil {
			fmt.Fprintf(os.Stderr, "worker error: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if len(args) == 0 {
		printHelp()
		os.Exit(0)
	}
	cmd := args[0]
	switch cmd {
	case "start":
		if err := cmdStart(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			os.Exit(1)
		}
	case "status":
		if err := cmdStatus(); err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			os.Exit(1)
		}
	case "stop":
		if err := cmdStop(); err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			os.Exit(1)
		}
	case "logs":
		if err := cmdLogs(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			os.Exit(1)
		}
	case "upload":
		if err := cmdUpload(); err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			os.Exit(1)
		}
	case "reoauth", "reauth", "relogin":
		if err := cmdReoauth(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			os.Exit(1)
		}
	case "config":
		if err := cmdConfig(); err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			os.Exit(1)
		}
	case "help", "-h", "--help":
		printHelp()
	case "version", "-v", "--version":
		fmt.Println("grok-reg 0.1.0")
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n\n", cmd)
		printHelp()
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Print(`grok — Grok 注册 + OAuth 二合一 CLI

用法:
  grok start                      交互：询问注册数量与并发线程(1-8)
  grok start -t N --thread M      目标 N 个 CPA 成功；M 并发线程
  grok start -t N -j M            同上（-j 为 --thread 简写）
  grok status                     查看运行状态与进度
  grok stop                       立即停止注册机
  grok logs [-f] [--debug|--info|--warn|--error]
                                  查看最近一次运行日志；-f 实时跟踪
  grok upload                     选择最近 run 的 CPA JSON 上传到 Management API
  grok reoauth <path>             对 inspection/CPA/accounts 重登并写出新 CPA
  grok config                     打开 ~/.grok/config.env（并刷新 config.env.example）
  grok help                       显示帮助

说明:
  -t / --target   目标账号数 = 探活成功写入 CPA/ 的数量 (1-10000)
  --thread / -j   并发注册/Turnstile 线程数 (1-8)，默认交互回车=2（较稳）
  logs 等级:      默认 --info（隐藏 DBG）；--debug 显示全部；--warn / --error 更严
                  例: grok logs -f --debug
  默认节奏:       OAUTH_MIN_INTERVAL_SEC=6  PROBE_WARMUP_SEC=5  OAUTH_RETRY_SEC=60
  reoauth:        优先 refresh_token；否则 SSO device；配置了 CPA 上传则自动入库
  升级后请查看 ~/.grok/config.env.example 了解新增配置项

数据目录: ~/.grok/ (可用 GROK_HOME 覆盖)
  输出:     ~/.grok/outputs/<yyyymmdd-HHMMSS>/{SSO,CPA,grok2api}/
            grok2api/tokens.txt = 单行 SSO token（无密码，供 grok2api）
  grok stop 在 CLEARANCE_AUTO_STOP=1 时会同时 docker compose stop 清障栈
`)
}

func paths() (home.Paths, error) {
	p, err := home.Resolve()
	if err != nil {
		return p, err
	}
	if err := p.EnsureBase(); err != nil {
		return p, err
	}
	return p, nil
}

func cmdStart(args []string) error {
	target := 0
	threads := 0
	targetSet, threadSet := false, false

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-t" || a == "--target":
			if i+1 >= len(args) {
				return fmt.Errorf("-t 需要数字参数")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				return fmt.Errorf("无效目标: %s", args[i+1])
			}
			target, err = config.ClampTarget(n)
			if err != nil {
				return err
			}
			targetSet = true
			i++
		case a == "--thread" || a == "--threads" || a == "-j":
			if i+1 >= len(args) {
				return fmt.Errorf("%s 需要数字参数 (1-8)", a)
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				return fmt.Errorf("无效线程数: %s", args[i+1])
			}
			threads, err = config.ClampThreads(n)
			if err != nil {
				return err
			}
			threadSet = true
			i++
		case strings.HasPrefix(a, "-t") && len(a) > 2 && a[2] >= '0' && a[2] <= '9':
			n, err := strconv.Atoi(strings.TrimPrefix(a, "-t"))
			if err != nil {
				return fmt.Errorf("无效 -t: %s", a)
			}
			target, err = config.ClampTarget(n)
			if err != nil {
				return err
			}
			targetSet = true
		case strings.HasPrefix(a, "-j") && len(a) > 2:
			n, err := strconv.Atoi(strings.TrimPrefix(a, "-j"))
			if err != nil {
				return fmt.Errorf("无效 -j: %s", a)
			}
			threads, err = config.ClampThreads(n)
			if err != nil {
				return err
			}
			threadSet = true
		default:
			return fmt.Errorf("未知参数: %s（用法: grok start -t N --thread M）", a)
		}
	}

	p, err := paths()
	if err != nil {
		return err
	}
	// Always refresh example so upgrades surface new keys.
	_ = config.SyncExample(p.Root)

	// already running?
	if pid, err := daemon.ReadPID(p.PID); err == nil && daemon.PIDAlive(pid) {
		return fmt.Errorf("注册机已经在运行 (PID %d)，先 grok status / grok stop", pid)
	}

	// config (email mode etc.)
	if _, err := os.Stat(p.Config); os.IsNotExist(err) {
		if _, err := config.InteractiveSetup(p.Config); err != nil {
			return err
		}
	}

	// Interactive prompts when flags omitted
	reader := bufio.NewReader(os.Stdin)
	if !targetSet {
		fmt.Print("注册数量 (探活成功计 1，1-10000) [10]: ")
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			target = 10
		} else {
			n, err := strconv.Atoi(line)
			if err != nil {
				return fmt.Errorf("无效数量: %s", line)
			}
			target, err = config.ClampTarget(n)
			if err != nil {
				return err
			}
		}
	}
	if !threadSet {
		fmt.Print("并发线程数 (Turnstile/注册并行，1-8) [2]: ")
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			threads = 2
		} else {
			n, err := strconv.Atoi(line)
			if err != nil {
				return fmt.Errorf("无效线程数: %s", line)
			}
			threads, err = config.ClampThreads(n)
			if err != nil {
				return err
			}
		}
	}

	runID := home.NewRunID()
	_ = os.MkdirAll(p.LogsDir, 0o700)
	logPath := filepath.Join(p.LogsDir, fmt.Sprintf("run-%s.log", runID))

	st := state.NewStore(p.State)
	_ = st.Set(func(s *state.Snapshot) {
		s.Status = state.StatusRunning
		s.RunID = runID
		s.Target = target
		s.Done = 0
		s.Phase = state.PhaseIdle
		s.PhaseDetail = "启动中"
		s.Workers = state.Workers{S: threads}
		s.LogPath = logPath
		s.OutputDir = filepath.Join(p.Outputs, runID)
		s.Error = ""
		s.PID = 0
	})

	pid, err := daemon.StartBackground(target, threads, runID)
	if err != nil {
		return err
	}
	if err := daemon.WritePID(p.PID, pid); err != nil {
		return err
	}
	_ = st.Set(func(s *state.Snapshot) { s.PID = pid })

	fmt.Printf("[✓] 注册机已后台启动\n")
	fmt.Printf("    PID:    %d\n", pid)
	fmt.Printf("    目标:   %d\n", target)
	fmt.Printf("    线程:   %d\n", threads)
	fmt.Printf("    Run:    %s\n", runID)
	fmt.Printf("    日志:   %s\n", logPath)
	fmt.Printf("    输出:   %s\n", filepath.Join(p.Outputs, runID))
	fmt.Printf("    配置:   %s  |  示例: %s\n", p.Config, config.ExamplePath(p.Root))
	fmt.Printf("    查看:   grok status  |  grok logs -f  |  grok config\n")
	return nil
}

func cmdConfig() error {
	p, err := paths()
	if err != nil {
		return err
	}
	_ = config.SyncExample(p.Root)
	// Ensure config exists
	if _, err := os.Stat(p.Config); os.IsNotExist(err) {
		if _, err := config.InteractiveSetup(p.Config); err != nil {
			return err
		}
	}
	fmt.Printf("配置文件: %s\n", p.Config)
	fmt.Printf("示例参考: %s（升级后自动刷新，含新增项说明）\n", config.ExamplePath(p.Root))

	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("VISUAL"))
	}
	// Prefer common editors
	candidates := []string{}
	if editor != "" {
		candidates = append(candidates, editor)
	}
	candidates = append(candidates, "nano", "vim", "vi", "nvim", "code", "open")
	var lastErr error
	for _, ed := range candidates {
		// `open` is macOS; use -t for textedit or just open path
		var cmd *os.File
		_ = cmd
		c := execEditor(ed, p.Config)
		if c == nil {
			continue
		}
		if err := c(); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr != nil {
		return fmt.Errorf("无法打开编辑器: %v；请手动编辑 %s", lastErr, p.Config)
	}
	return fmt.Errorf("未找到编辑器，请手动编辑 %s（可设置 EDITOR=nano）", p.Config)
}

func execEditor(editor, path string) func() error {
	editor = strings.TrimSpace(editor)
	if editor == "" {
		return nil
	}
	// split simple "code -w" style
	parts := strings.Fields(editor)
	bin := parts[0]
	args := append(parts[1:], path)
	if bin == "open" {
		// macOS: open with default app for .env, or TextEdit
		args = []string{"-e", path}
	}
	return func() error {
		// use os/exec via shelling — import already has no exec in main? need add
		return runCmd(bin, args...)
	}
}

func runWorker(args []string) error {
	target := 10
	threads := 2
	runID := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--worker":
			continue
		case "--target":
			if i+1 < len(args) {
				n, _ := strconv.Atoi(args[i+1])
				if n > 0 {
					target = n
				}
				i++
			}
		case "--threads", "--thread":
			if i+1 < len(args) {
				n, _ := strconv.Atoi(args[i+1])
				if n > 0 {
					threads = n
				}
				i++
			}
		case "--run-id":
			if i+1 < len(args) {
				runID = args[i+1]
				i++
			}
		}
	}
	var err error
	target, err = config.ClampTarget(target)
	if err != nil {
		return err
	}
	threads, err = config.ClampThreads(threads)
	if err != nil {
		// tolerate edge: clamp silently
		if threads < 1 {
			threads = 1
		}
		if threads > 8 {
			threads = 8
		}
	}

	p, err := paths()
	if err != nil {
		return err
	}
	_ = config.SyncExample(p.Root)

	unlock, err := daemon.TryLock(p.Lock)
	if err != nil {
		return err
	}
	defer unlock()

	if err := daemon.WritePID(p.PID, os.Getpid()); err != nil {
		return err
	}
	defer daemon.ClearPID(p.PID)

	cfg, err := config.Load(p.Config)
	if err != nil {
		return err
	}
	cfg.Target = target
	cfg.TurnstileWorkers = threads

	run, err := p.PrepareRun(runID)
	if err != nil {
		return err
	}
	log, err := logx.New(run.LogPath)
	if err != nil {
		return err
	}
	defer log.Close()

	st := state.NewStore(p.State)
	_ = st.Set(func(s *state.Snapshot) {
		s.Status = state.StatusRunning
		s.RunID = run.RunID
		s.Target = target
		s.PID = os.Getpid()
		s.LogPath = run.LogPath
		s.OutputDir = run.Root
		s.Workers = state.Workers{S: threads}
	})

	ctx := context.Background()
	err = pipeline.Run(ctx, pipeline.Options{
		Cfg:    cfg,
		Paths:  p,
		Run:    run,
		Target: target,
		Log:    log,
		Store:  st,
	})
	if err != nil {
		_ = st.Set(func(s *state.Snapshot) {
			s.Status = state.StatusError
			s.Error = err.Error()
			s.PhaseDetail = "错误退出"
			s.PID = 0
		})
		log.Errf("%v", err)
		return err
	}
	return nil
}

func cmdStatus() error {
	p, err := paths()
	if err != nil {
		return err
	}
	st := state.NewStore(p.State)
	snap, err := st.Load()
	if err != nil && !os.IsNotExist(err) {
		// no state yet
		fmt.Println("状态: 未运行")
		return nil
	}
	if os.IsNotExist(err) {
		fmt.Println("状态: 未运行")
		return nil
	}
	// reconcile pid
	if snap.Status == state.StatusRunning {
		if snap.PID == 0 {
			if pid, e := daemon.ReadPID(p.PID); e == nil {
				snap.PID = pid
			}
		}
		if snap.PID != 0 && !daemon.PIDAlive(snap.PID) {
			snap.Status = state.StatusStopped
			snap.PhaseDetail = "进程已结束"
			snap.PID = 0
		}
	}
	fmt.Print(daemon.FormatStatus(snap))
	return nil
}

func cmdStop() error {
	p, err := paths()
	if err != nil {
		return err
	}
	if err := daemon.Stop(p); err != nil {
		return err
	}
	st := state.NewStore(p.State)
	_ = st.Set(func(s *state.Snapshot) {
		s.Status = state.StatusStopped
		s.Phase = state.PhaseIdle
		s.PhaseDetail = "已手动停止"
		s.PID = 0
	})
	fmt.Println("[✓] 注册机已停止")
	// Worker may be SIGKILL'd before pipeline defer; always stop clearance when configured.
	stopClearanceStackOnStop(p)
	return nil
}

// stopClearanceStackOnStop mirrors CLEARANCE_AUTO_STOP for manual `grok stop`.
func stopClearanceStackOnStop(p home.Paths) {
	cfg, err := config.Load(p.Config)
	if err != nil {
		// Defaults: auto-stop on
		cfg = config.Defaults()
	}
	if !cfg.ClearanceAutoStop || !cfg.ClearanceEnabled {
		return
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.ClearanceMode))
	if mode == "never" {
		return
	}
	msg, err := clearance.StopStack(cfg.ClearanceComposeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[!] 清障栈停止失败: %v\n", err)
		return
	}
	if msg != "" {
		fmt.Printf("[✓] %s\n", msg)
	}
}

func cmdLogs(args []string) error {
	follow := false
	minLevel := logx.LevelInfo // default: hide DBG
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-f" || a == "--follow":
			follow = true
		case a == "--debug" || a == "-d" || a == "--verbose" || a == "-v":
			minLevel = logx.LevelDebug
		case a == "--info" || a == "-i":
			minLevel = logx.LevelInfo
		case a == "--warn" || a == "--warning" || a == "-w":
			minLevel = logx.LevelWarn
		case a == "--error" || a == "--err" || a == "-e":
			minLevel = logx.LevelError
		case a == "--level" || a == "-l":
			if i+1 >= len(args) {
				return fmt.Errorf("--level 需要参数: debug|info|warn|error")
			}
			i++
			lv, err := logx.ParseLevel(args[i])
			if err != nil {
				return err
			}
			minLevel = lv
		case strings.HasPrefix(a, "--level="):
			lv, err := logx.ParseLevel(strings.TrimPrefix(a, "--level="))
			if err != nil {
				return err
			}
			minLevel = lv
		case a == "-h" || a == "--help":
			fmt.Print(`grok logs — 查看 / 跟踪运行日志

用法:
  grok logs                     打印最近日志（默认 ≥ info，隐藏 DBG）
  grok logs -f                  实时跟踪
  grok logs -f --debug          显示 DBG 及全部
  grok logs --warn              仅警告与错误
  grok logs --level=error       仅错误
  grok logs -l debug -f         同上

说明: 磁盘日志始终完整写入；等级只过滤终端显示。
`)
			return nil
		default:
			return fmt.Errorf("未知 logs 参数: %s（见 grok logs --help）", a)
		}
	}
	p, err := paths()
	if err != nil {
		return err
	}
	st := state.NewStore(p.State)
	snap, _ := st.Load()
	path := snap.LogPath
	if path == "" {
		path = latestLog(p.LogsDir)
	}
	if path == "" {
		return fmt.Errorf("没有日志文件")
	}
	levelName := "info"
	switch {
	case minLevel <= logx.LevelDebug:
		levelName = "debug"
	case minLevel <= logx.LevelInfo:
		levelName = "info"
	case minLevel <= logx.LevelWarn:
		levelName = "warn"
	default:
		levelName = "error"
	}

	if !follow {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Print(logx.FilterText(string(data), minLevel))
		return nil
	}
	fmt.Fprintf(os.Stderr, "跟踪 %s  level≥%s  (Ctrl-C 退出)\n", path, levelName)
	var offset int64
	if fi, err := os.Stat(path); err == nil {
		// show last ~8k then filter (more room when DBG hidden)
		chunk := int64(8192)
		if minLevel <= logx.LevelDebug {
			chunk = 16384
		}
		offset = fi.Size() - chunk
		if offset < 0 {
			offset = 0
		}
	}
	var carry string // incomplete line across reads
	for {
		f, err := os.Open(path)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if _, err := f.Seek(offset, 0); err != nil {
			_ = f.Close()
			time.Sleep(500 * time.Millisecond)
			continue
		}
		buf := make([]byte, 8192)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				offset += int64(n)
				carry += string(buf[:n])
				for {
					i := strings.IndexByte(carry, '\n')
					if i < 0 {
						break
					}
					line := carry[:i]
					carry = carry[i+1:]
					if logx.KeepLine(line, minLevel) {
						fmt.Println(line)
					}
				}
			}
			if err != nil {
				break
			}
		}
		_ = f.Close()
		time.Sleep(400 * time.Millisecond)
	}
}

func latestLog(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var best string
	var bestT time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "run-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(bestT) {
			bestT = info.ModTime()
			best = filepath.Join(dir, e.Name())
		}
	}
	return best
}

func cmdReoauth(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(`grok reoauth — 对已有账号重新拿 CPA 凭证（重登）

用法:
  grok reoauth <path> [选项]

<path> 可以是:
  · inspection 导出 JSON（含 results[].email，如 quota_exhausted 报告）
  · 单个 CPA JSON（xai-*.json，含 refresh_token）
  · CPA/ 目录 或 整个 run 目录（含 SSO/accounts.txt）
  · accounts.txt（email:password:sso）
  · auth-sessions.jsonl

策略:
  1) 有 refresh_token → OAuth refresh_token 换新 token（无需邮箱验证码）
  2) 否则有 sso → device OAuth（与注册机相同）
  3) inspection 仅 email 时，自动在 ~/.grok/outputs 里按 email 查找历史 CPA/SSO
  4) 若 config 启用了 CPA 上传（CPA_UPLOAD_ENABLED + KEY），成功后自动入库 Management API

选项:
  --thread N / -j N   并发 (1-8，默认 2)
  --out DIR           CPA 输出目录（默认 ~/.grok/outputs/reoauth-<时间>/CPA）
  --no-lookup         不扫描本地 outputs 补全凭证
  --no-probe          写出前不做 cli-chat-proxy 探活
  --interval SEC      两次请求最小间隔（默认 2）
  --upload            强制上传（即使 CPA_UPLOAD_ENABLED=0，仍需 KEY）
  --no-upload         禁止上传（覆盖 config）

示例:
  grok reoauth ./grok-inspection-quota_exhausted-....json
  grok reoauth ~/.grok/outputs/20260723-232838/CPA --thread 3
  grok reoauth ~/.grok/outputs/20260723-232838/SSO/accounts.txt --out /tmp/cpa-re
`)
		return nil
	}

	path := ""
	threads := 2
	outDir := ""
	lookup := true
	probe := true
	intervalSec := 2.0
	uploadForce := false
	uploadOff := false

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--thread" || a == "-j":
			if i+1 >= len(args) {
				return fmt.Errorf("%s 需要数字", a)
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 1 || n > 8 {
				return fmt.Errorf("无效线程: %s", args[i+1])
			}
			threads = n
			i++
		case strings.HasPrefix(a, "--thread="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--thread="))
			if err != nil || n < 1 || n > 8 {
				return fmt.Errorf("无效线程: %s", a)
			}
			threads = n
		case a == "--out" || a == "-o":
			if i+1 >= len(args) {
				return fmt.Errorf("%s 需要目录", a)
			}
			outDir = args[i+1]
			i++
		case strings.HasPrefix(a, "--out="):
			outDir = strings.TrimPrefix(a, "--out=")
		case a == "--no-lookup":
			lookup = false
		case a == "--no-probe":
			probe = false
		case a == "--upload":
			uploadForce = true
		case a == "--no-upload":
			uploadOff = true
		case a == "--interval":
			if i+1 >= len(args) {
				return fmt.Errorf("--interval 需要秒数")
			}
			v, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil || v < 0 {
				return fmt.Errorf("无效 interval: %s", args[i+1])
			}
			intervalSec = v
			i++
		case strings.HasPrefix(a, "--interval="):
			v, err := strconv.ParseFloat(strings.TrimPrefix(a, "--interval="), 64)
			if err != nil || v < 0 {
				return fmt.Errorf("无效 interval: %s", a)
			}
			intervalSec = v
		case a == "-h" || a == "--help":
			return cmdReoauth(nil)
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("未知参数: %s（见 grok reoauth -h）", a)
		default:
			if path == "" {
				path = a
			} else {
				return fmt.Errorf("多余参数: %s", a)
			}
		}
	}
	if path == "" {
		return fmt.Errorf("需要 path（见 grok reoauth -h）")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("路径不存在: %s", abs)
	}

	p, err := paths()
	if err != nil {
		return err
	}
	cfg, err := config.Load(p.Config)
	if err != nil {
		return err
	}
	proxy := strings.TrimSpace(cfg.RegisterProxy)
	if proxy == "" {
		proxy = strings.TrimSpace(cfg.HTTPSProxy)
	}

	accs, err := reoauth.ParsePath(abs)
	if err != nil {
		return err
	}
	if len(accs) == 0 {
		return fmt.Errorf("未解析到任何账号: %s", abs)
	}
	fmt.Printf("[*] 解析到 %d 个账号 from %s\n", len(accs), abs)

	if outDir == "" {
		runID := "reoauth-" + home.NewRunID()
		rd, err := p.PrepareRun(runID)
		if err != nil {
			return err
		}
		outDir = rd.CPA
		fmt.Printf("[*] 输出目录: %s\n", rd.Root)
	} else {
		outDir, _ = filepath.Abs(outDir)
		if err := os.MkdirAll(outDir, 0o700); err != nil {
			return err
		}
		fmt.Printf("[*] 输出 CPA: %s\n", outDir)
	}

	var lookupRoots []string
	if lookup {
		lookupRoots = []string{p.Outputs}
	}

	// CPA Management upload: same config as register pipeline.
	uploadEnabled := cfg.CPAUploadEnabled
	if uploadForce {
		uploadEnabled = true
	}
	if uploadOff {
		uploadEnabled = false
	}
	var uploader *cpa.Uploader
	if uploadEnabled {
		if strings.TrimSpace(cfg.CPAManagementKey) == "" {
			fmt.Println("[!] CPA 上传已启用但未配置 CPA_MANAGEMENT_KEY，跳过入库")
		} else {
			base := cfg.CPAManagementBase
			if strings.TrimSpace(base) == "" {
				base = "http://127.0.0.1:8317/v0/management"
			}
			uploader = cpa.NewUploader(cpa.UploadConfig{
				Enabled:      true,
				BaseURL:      base,
				Key:          cfg.CPAManagementKey,
				TimeoutSec:   cfg.CPAUploadTimeoutSec,
				Retries:      cfg.CPAUploadRetries,
				NameTemplate: cfg.CPAUploadNameTemplate,
				Verify:       cfg.CPAUploadVerify,
				Mode:         cfg.CPAUploadMode,
			}, func(f string, a ...any) {
				fmt.Printf("[cpa] "+f+"\n", a...)
			})
			if uploader.Enabled() {
				fmt.Printf("[*] CPA 自动入库: %s\n", cpa.NormalizeManagementBase(base))
			}
		}
	}

	ctx := context.Background()
	_, err = reoauth.Run(ctx, accs, reoauth.Options{
		Proxy:       proxy,
		OutCPA:      outDir,
		Workers:     threads,
		MinInterval: time.Duration(intervalSec * float64(time.Second)),
		Probe:       probe,
		ProbeWarmup: cfg.ProbeWarmupSec,
		LookupRoots: lookupRoots,
		Secret:      cpa.DefaultSecret(),
		Uploader:    uploader,
		OutLog: func(f string, a ...any) {
			fmt.Printf(f+"\n", a...)
		},
	})
	return err
}

func cmdUpload() error {
	p, err := paths()
	if err != nil {
		return err
	}
	cfg, err := config.Load(p.Config)
	if err != nil {
		return err
	}
	// env overrides
	if v := os.Getenv("CPA_UPLOAD_ENABLED"); v != "" {
		cfg.CPAUploadEnabled = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	}
	if v := os.Getenv("CPA_MANAGEMENT_BASE"); v != "" {
		cfg.CPAManagementBase = v
	}
	if v := os.Getenv("CPA_MANAGEMENT_KEY"); v != "" {
		cfg.CPAManagementKey = v
	}
	// interactive upload always allowed if key+base set
	if strings.TrimSpace(cfg.CPAManagementKey) == "" {
		return fmt.Errorf("未配置 CPA_MANAGEMENT_KEY（在 ~/.grok/config.env 或环境变量中设置）")
	}
	if strings.TrimSpace(cfg.CPAManagementBase) == "" {
		cfg.CPAManagementBase = "http://localhost:8317/v0/management"
	}

	runs, err := cpa.ListRunDirs(p.Outputs, 10)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return fmt.Errorf("outputs 下没有注册结果目录")
	}

	fmt.Println("最近注册 run（最多 10 个）:")
	type item struct {
		dir   string
		name  string
		files []string
	}
	var items []item
	for i, dir := range runs {
		files, _ := cpa.CollectCPAJSON(dir)
		name := filepath.Base(dir)
		items = append(items, item{dir: dir, name: name, files: files})
		fmt.Printf("  [%d] %s  CPA文件=%d\n", i+1, name, len(files))
	}
	fmt.Print("选择要上传的序号（如 1 或 1,2,3；回车取消）: ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		fmt.Println("已取消")
		return nil
	}
	var selected []int
	for _, part := range strings.Split(line, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// allow ranges? no — only comma list
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 || n > len(items) {
			return fmt.Errorf("无效序号: %s", part)
		}
		selected = append(selected, n-1)
	}
	if len(selected) == 0 {
		fmt.Println("未选择")
		return nil
	}

	up := cpa.NewUploader(cpa.UploadConfig{
		Enabled:      true,
		BaseURL:      cfg.CPAManagementBase,
		Key:          cfg.CPAManagementKey,
		TimeoutSec:   cfg.CPAUploadTimeoutSec,
		Retries:      cfg.CPAUploadRetries,
		NameTemplate: cfg.CPAUploadNameTemplate,
		Verify:       cfg.CPAUploadVerify,
		Mode:         cfg.CPAUploadMode,
	}, func(f string, a ...any) {
		fmt.Printf(f+"\n", a...)
	})

	var okN, failN, skipN int
	for _, idx := range selected {
		it := items[idx]
		if len(it.files) == 0 {
			fmt.Printf("[!] %s 无 CPA json，跳过\n", it.name)
			skipN++
			continue
		}
		fmt.Printf("[*] 上传 %s (%d 个文件)...\n", it.name, len(it.files))
		for _, f := range it.files {
			res := up.UploadFile(f)
			if res.OK {
				okN++
			} else {
				failN++
			}
		}
	}
	fmt.Printf("[✓] 完成 ok=%d fail=%d skip_runs=%d\n", okN, failN, skipN)
	return nil
}
