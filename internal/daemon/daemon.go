package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/grok-free-register/grok-reg/internal/home"
	"github.com/grok-free-register/grok-reg/internal/state"
)

const workerFlag = "--worker"

// IsWorker returns true when this process is the background worker.
func IsWorker() bool {
	for _, a := range os.Args[1:] {
		if a == workerFlag {
			return true
		}
	}
	return false
}

func PIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks existence without killing.
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

func ReadPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return 0, fmt.Errorf("empty pid file")
	}
	return strconv.Atoi(s)
}

func WritePID(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

func ClearPID(path string) {
	_ = os.Remove(path)
}

// TryLock creates an exclusive lock file. Returns unlock func.
func TryLock(lockPath string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("注册机已经在运行（无法获取锁）")
	}
	unlock := func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
	return unlock, nil
}

// StartBackground re-execs self with --worker and returns child PID.
// threads is concurrent mint/register workers (1–8).
func StartBackground(target, threads int, runID string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	if threads < 1 {
		threads = 2
	}
	args := []string{
		workerFlag,
		"--target", strconv.Itoa(target),
		"--threads", strconv.Itoa(threads),
	}
	if runID != "" {
		args = append(args, "--run-id", runID)
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	cmd.Env = os.Environ()
	// Detach from controlling terminal.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	// Release child so parent can exit without waiting.
	_ = cmd.Process.Release()
	return pid, nil
}

func Stop(paths home.Paths) error {
	pid, err := ReadPID(paths.PID)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("注册机未在运行")
		}
		return err
	}
	if !PIDAlive(pid) {
		ClearPID(paths.PID)
		store := state.NewStore(paths.State)
		_ = store.Set(func(s *state.Snapshot) {
			s.Status = state.StatusStopped
			s.Phase = state.PhaseIdle
			s.PhaseDetail = "已停止"
			s.PID = 0
		})
		return fmt.Errorf("注册机未在运行（残留 PID 已清理）")
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	// Graceful first — pipeline may drain OAuth / CPA upload before exit.
	_ = proc.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if !PIDAlive(pid) {
			ClearPID(paths.PID)
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = proc.Signal(syscall.SIGKILL)
	time.Sleep(300 * time.Millisecond)
	ClearPID(paths.PID)
	return nil
}

func FormatStatus(snap state.Snapshot) string {
	alive := PIDAlive(snap.PID)
	status := snap.Status
	if status == state.StatusRunning && !alive && snap.PID != 0 {
		status = state.StatusError
		if snap.Error == "" {
			snap.Error = "进程已退出但状态未更新"
		}
	}
	if status == "" {
		status = state.StatusStopped
	}

	var b strings.Builder
	switch status {
	case state.StatusRunning:
		b.WriteString("状态: 运行中\n")
	case state.StatusError:
		b.WriteString("状态: 错误\n")
	default:
		b.WriteString("状态: 未运行\n")
	}
	if snap.RunID != "" {
		b.WriteString(fmt.Sprintf("Run:  %s\n", snap.RunID))
	}
	if snap.Target > 0 || snap.Done > 0 {
		b.WriteString(fmt.Sprintf("进度: %d/%d\n", snap.Done, snap.Target))
	}
	w := snap.Workers
	if w.S+w.P+w.C+w.OAuth > 0 {
		b.WriteString(fmt.Sprintf("线程: S=%d P=%d C=%d OAuth=%d\n", w.S, w.P, w.C, w.OAuth))
	}
	if snap.PhaseDetail != "" {
		b.WriteString(fmt.Sprintf("当前: %s\n", snap.PhaseDetail))
	} else if snap.Phase != "" && snap.Phase != state.PhaseIdle {
		b.WriteString(fmt.Sprintf("当前: %s\n", snap.Phase))
	}
	if snap.SSOCount > 0 || snap.OAuthCount > 0 {
		b.WriteString(fmt.Sprintf("统计: SSO=%d OAuth=%d fail=%d\n", snap.SSOCount, snap.OAuthCount, snap.FailCount))
	}
	if snap.RatePerMin > 0 {
		b.WriteString(fmt.Sprintf("速率: %.1f/分\n", snap.RatePerMin))
	}
	if snap.PID > 0 {
		b.WriteString(fmt.Sprintf("PID:  %d\n", snap.PID))
	}
	if snap.LogPath != "" {
		b.WriteString(fmt.Sprintf("日志: %s\n", snap.LogPath))
	}
	if snap.OutputDir != "" {
		b.WriteString(fmt.Sprintf("输出: %s\n", snap.OutputDir))
	}
	if snap.Error != "" {
		b.WriteString(fmt.Sprintf("错误: %s\n", snap.Error))
	}
	return b.String()
}
