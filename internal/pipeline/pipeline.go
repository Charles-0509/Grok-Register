package pipeline

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/grok-free-register/grok-reg/internal/clearance"
	"github.com/grok-free-register/grok-reg/internal/config"
	"github.com/grok-free-register/grok-reg/internal/cpa"
	"github.com/grok-free-register/grok-reg/internal/email"
	"github.com/grok-free-register/grok-reg/internal/home"
	"github.com/grok-free-register/grok-reg/internal/inventory"
	"github.com/grok-free-register/grok-reg/internal/logx"
	"github.com/grok-free-register/grok-reg/internal/oauth"
	"github.com/grok-free-register/grok-reg/internal/protocol"
	"github.com/grok-free-register/grok-reg/internal/state"
	"github.com/grok-free-register/grok-reg/internal/turnstile"
)

type QItem struct {
	Email    string
	Password string
	Code     string
	Handle   email.Handle
}

type SSOJob struct {
	Email    string
	Password string
	SSO      string
}

type Options struct {
	Cfg    config.Config
	Paths  home.Paths
	Run    home.RunDirs
	Target int
	Log    *logx.Logger
	Store  *state.Store
}

type Engine struct {
	opt Options

	cm       *clearance.Manager
	xai      *protocol.Client
	mail     *email.Provider
	turn     turnstile.Provider
	oauth    *oauth.Client
	inv      *inventory.Inventory[string, QItem]
	phys     *inventory.Semaphore
	qPending *inventory.Semaphore

	oauthCh  chan SSOJob
	uploader *cpa.Uploader

	done     atomic.Int64 // CPA successes (counts toward -t)
	reserved atomic.Int64 // in-flight accounts (email→register→oauth→probe)
	ssoN     atomic.Int64
	oaN      atomic.Int64
	fail     atomic.Int64

	// Global OAuth pacing (shared by all oauth workers) — avoids dual-worker rate_limited.
	oauthGateMu    sync.Mutex
	oauthLastStart time.Time

	start    time.Time
	wgReg    sync.WaitGroup // S/P/C
	wgOAuth  sync.WaitGroup
	wgAux    sync.WaitGroup // status ticker etc
	wgUpload sync.WaitGroup // async CPA management uploads
}

// remainingCapacity = target - done - reserved (how many new accounts may start).
func (e *Engine) remainingCapacity() int {
	n := e.opt.Target - int(e.done.Load()) - int(e.reserved.Load())
	if n < 0 {
		return 0
	}
	return n
}

// tryReserve claims one pipeline seat for a new account attempt.
func (e *Engine) tryReserve() bool {
	for {
		d := e.done.Load()
		r := e.reserved.Load()
		if d+r >= int64(e.opt.Target) {
			return false
		}
		if e.reserved.CompareAndSwap(r, r+1) {
			return true
		}
	}
}

func (e *Engine) releaseReserve() {
	for {
		r := e.reserved.Load()
		if r <= 0 {
			return
		}
		if e.reserved.CompareAndSwap(r, r-1) {
			return
		}
	}
}

// tryComplete moves a reserved seat into done. Returns (newDone, ok).
// ok=false means target already met (caller should discard extra success).
func (e *Engine) tryComplete() (int64, bool) {
	for {
		d := e.done.Load()
		if d >= int64(e.opt.Target) {
			e.releaseReserve()
			return d, false
		}
		if e.done.CompareAndSwap(d, d+1) {
			e.releaseReserve()
			return d + 1, true
		}
	}
}

func Run(ctx context.Context, opt Options) error {
	e := &Engine{
		opt:     opt,
		oauthCh: make(chan SSOJob, 64),
		start:   time.Now(),
	}
	return e.run(ctx)
}

func (e *Engine) run(ctx context.Context) error {
	cfg := e.opt.Cfg
	log := e.opt.Log
	st := e.opt.Store

	config.ApplyProxyEnv(cfg)

	sWorkers, pWorkers, cWorkers, oauthWorkers, physCap := deriveWorkers(cfg)
	e.phys = inventory.NewSemaphore(physCap)
	// Pending email codes in flight: follow --thread (was hard cap 6 → reserved flood).
	qPend := sWorkers + 1
	if qPend < 2 {
		qPend = 2
	}
	if qPend > 4 {
		qPend = 4
	}
	if cfg.Target > 0 && cfg.Target < qPend {
		qPend = cfg.Target
	}
	e.qPending = inventory.NewSemaphore(qPend)
	tSlots, qSlots := sWorkers+1, sWorkers+1
	if tSlots < 2 {
		tSlots, qSlots = 2, 2
	}
	if tSlots > 5 {
		tSlots, qSlots = 5, 5
	}
	if cfg.Target > 0 && cfg.Target < tSlots {
		tSlots, qSlots = cfg.Target, cfg.Target
	}
	e.inv = inventory.New[string, QItem](tSlots, qSlots)
	log.Infof("workers S=%d P=%d C=%d OAuth=%d phys=%d q_pending=%d t/q_slots=%d", sWorkers, pWorkers, cWorkers, oauthWorkers, physCap, qPend, tSlots)

	_ = st.Set(func(s *state.Snapshot) {
		s.Status = state.StatusRunning
		s.RunID = e.opt.Run.RunID
		s.Target = e.opt.Target
		s.Done = 0
		s.Phase = state.PhaseClearance
		s.PhaseDetail = "清障预热中"
		s.Workers = state.Workers{S: sWorkers, P: pWorkers, C: cWorkers, OAuth: oauthWorkers}
		s.PID = os.Getpid()
		s.StartedAt = e.start.UTC().Format(time.RFC3339)
		s.LogPath = e.opt.Run.LogPath
		s.OutputDir = e.opt.Run.Root
		s.Error = ""
	})

	// Clearance mode: auto (protocol first) | always | never
	clearMode := strings.ToLower(strings.TrimSpace(cfg.ClearanceMode))
	if clearMode == "" {
		clearMode = "auto"
	}
	if !cfg.ClearanceEnabled {
		clearMode = "never"
	}
	stackStarted := false
	stopStack := func() {
		if !stackStarted || !cfg.ClearanceAutoStop {
			return
		}
		sm, serr := clearance.StopStack(cfg.ClearanceComposeDir)
		if serr != nil {
			log.Warnf("[clearance] 自动停止失败: %v", serr)
		} else {
			log.Infof("[clearance] %s", sm)
		}
	}
	defer stopStack()

	ensureClearance := func(reason string) {
		if clearMode == "never" {
			return
		}
		log.Infof("[clearance] 拉起清障栈 (%s)…", reason)
		msg, err := clearance.EnsureStack(cfg.ClearanceComposeDir, 40080, 8191)
		if err != nil {
			log.Warnf("[clearance] 自动拉起失败: %v", err)
			return
		}
		stackStarted = true
		log.Infof("[clearance] %s", msg)
		e.cm = clearance.NewManager(cfg.FlareSolverrURL, cfg.ClearanceProxy, cfg.ClearanceURLs)
		if msg2, err2 := e.cm.Prewarm(); err2 != nil {
			// Critical: accounts/auth ERR often correlates with OAuth invalid_grant later.
			log.Warnf("[clearance] 预热异常: %v | %s", err2, msg2)
			log.Warn("[clearance] 若 accounts.x.ai/auth.x.ai 为 ERR，OAuth 很可能全部 invalid_grant")
		} else {
			log.Infof("[clearance] %s", msg2)
		}
	}

	// If config points REGISTER_PROXY at local Privoxy (40080) but stack is down,
	// start it FIRST — otherwise warm spams chrome_124/120 with connection refused.
	proxyNeedsStack := clearance.LocalClearanceProxyDown(cfg.RegisterProxy, 40080)

	switch clearMode {
	case "always":
		log.Info("[clearance] CLEARANCE_MODE=always")
		ensureClearance("always")
	case "never":
		log.Info("[clearance] CLEARANCE_MODE=never（协议 TLS 直连/代理，无 Docker 清障）")
		if proxyNeedsStack {
			log.Warn("[clearance] REGISTER_PROXY 指向 :40080 但清障未运行，且 MODE=never — 请改代理或改为 auto/always")
		}
	default:
		log.Info("[clearance] CLEARANCE_MODE=auto（协议优先，CF 拦截时再拉清障）")
		if proxyNeedsStack && cfg.ClearanceEnabled {
			log.Info("[clearance] 检测到 REGISTER_PROXY→:40080 未监听，先起清障再 warm")
			ensureClearance("proxy_down")
		}
	}
	if cfg.ClearanceAutoStop && clearMode != "never" {
		log.Info("[clearance] CLEARANCE_AUTO_STOP=1：本 run 若拉起栈，结束时将 stop")
	}

	var err error
	imp := cfg.CFImpersonate
	if imp == "" {
		imp = "chrome_131"
	}
	e.xai, err = protocol.NewClientOpts(protocol.ClientOptions{
		Proxy:               cfg.RegisterProxy,
		Clearance:           e.cm,
		Impersonate:         imp,
		ImpersonateFallback: protocol.FallbackProfiles(cfg.CFImpersonateFallback),
	})
	if err != nil {
		return err
	}
	log.Infof("[cf] TLS impersonate=%s fallback=%s proxy=%v", e.xai.Profile(), cfg.CFImpersonateFallback, cfg.RegisterProxy != "")

	e.mail = email.New(email.Config{
		Mode:              cfg.EmailMode,
		Domain:            cfg.EmailDomain,
		API:               cfg.EmailAPI,
		LOLRetries:        cfg.TempmailLOLRetries,
		LOLIntervalMS:     cfg.TempmailLOLIntervalMS,
		TestmailAPIKey:    cfg.TestmailAPIKey,
		TestmailNamespace: cfg.TestmailNamespace,
		TestmailDomain:    cfg.TestmailDomain,
	})
	if cfg.EmailMode == config.EmailTestmail {
		log.Infof("Email mode=testmail namespace=%s domain=%s", cfg.TestmailNamespace, cfg.TestmailDomain)
	} else {
		log.Infof("Email mode=%s", cfg.EmailMode)
	}
	tsMode := cfg.TurnstileMode
	if tsMode == "" {
		tsMode = "offscreen"
	}
	e.turn = turnstile.New(turnstile.Options{
		Provider: cfg.TurnstileProvider,
		LiteURL:  cfg.LiteSolverURL,
		Proxy:    cfg.RegisterProxy,
		Clear:    e.cm,
		Workers:  sWorkers, // parallel S = pool slots
		Mode:     tsMode,
	})
	if c, ok := e.turn.(turnstile.Closer); ok {
		defer c.Close()
	}
	log.Infof("Turnstile provider=%s mode=%s workers=%d (pool → one-shot → chromedp)", e.turn.Name(), tsMode, sWorkers)
	log.Infof("Turnstile mint: python=%s pool=%s script=%s", turnstile.DetectedPython(), turnstile.DetectedPoolScript(), turnstile.DetectedScript())
	e.uploader = cpa.NewUploader(cpa.UploadConfig{
		Enabled:      cfg.CPAUploadEnabled,
		BaseURL:      cfg.CPAManagementBase,
		Key:          cfg.CPAManagementKey,
		TimeoutSec:   cfg.CPAUploadTimeoutSec,
		Retries:      cfg.CPAUploadRetries,
		NameTemplate: cfg.CPAUploadNameTemplate,
		Verify:       cfg.CPAUploadVerify,
		Mode:         cfg.CPAUploadMode,
	}, func(f string, a ...any) {
		log.Infof(f, a...)
	})
	if e.uploader.Enabled() {
		log.Infof("CPA upload enabled base=%s", cfg.CPAManagementBase)
	}
	e.oauth, err = oauth.NewClient(cfg.RegisterProxy, e.cm, time.Duration(cfg.OAuthRetrySec)*time.Second)
	if err != nil {
		return err
	}

	_ = st.Set(func(s *state.Snapshot) {
		s.Phase = state.PhaseRegister
		s.PhaseDetail = "获取注册配置"
	})
	log.Info("Fetching signup config (protocol warm)…")
	scfg, err := e.xai.FetchConfig()
	if err != nil {
		code := protocol.CodeOf(err)
		errStr := err.Error()
		proxyDead := strings.Contains(errStr, "connection refused") ||
			strings.Contains(errStr, "40080") ||
			strings.Contains(errStr, "connect: connection refused")
		log.Warnf("[cf] warm failed code=%s err=%v", code, err)

		// Proxy dead → start clearance immediately, skip fingerprint spam.
		if proxyDead && clearMode == "auto" && cfg.ClearanceEnabled {
			log.Warn("[cf] 代理不可达 (connection refused)，跳过 impersonate 回退，直接拉清障")
			ensureClearance("proxy_refused")
			e.xai, err = protocol.NewClientOpts(protocol.ClientOptions{
				Proxy:       cfg.RegisterProxy,
				Clearance:   e.cm,
				Impersonate: imp,
			})
			if err != nil {
				return err
			}
			scfg, err = e.xai.FetchConfig()
		} else {
			// Protocol-first: CF block → try profile fallbacks, then clearance auto
			tried := map[string]struct{}{e.xai.Profile(): {}}
			for _, fb := range protocol.FallbackProfiles(cfg.CFImpersonateFallback) {
				if _, ok := tried[fb]; ok {
					continue
				}
				tried[fb] = struct{}{}
				log.Infof("[cf] try impersonate fallback=%s", fb)
				if rerr := e.xai.RecreateWithProfile(fb); rerr != nil {
					log.Warnf("[cf] recreate %s: %v", fb, rerr)
					continue
				}
				scfg, err = e.xai.FetchConfig()
				if err == nil {
					log.Infof("[cf] warm ok profile=%s", e.xai.Profile())
					break
				}
				log.Warnf("[cf] fallback %s failed: %v", fb, err)
				if strings.Contains(err.Error(), "connection refused") {
					log.Warn("[cf] 仍是 connection refused，停止 fallback")
					break
				}
			}
			if err != nil && clearMode == "auto" {
				ensureClearance("cf_blocked")
				e.xai, err = protocol.NewClientOpts(protocol.ClientOptions{
					Proxy:       cfg.RegisterProxy,
					Clearance:   e.cm,
					Impersonate: e.xai.Profile(),
				})
				if err != nil {
					return err
				}
				scfg, err = e.xai.FetchConfig()
			}
		}
	}
	// Rebuild oauth client after clearance manager may have been attached.
	if e.cm != nil {
		if oc, oerr := oauth.NewClient(cfg.RegisterProxy, e.cm, time.Duration(cfg.OAuthRetrySec)*time.Second); oerr == nil {
			e.oauth = oc
		}
	}
	if err != nil {
		_ = st.Set(func(s *state.Snapshot) {
			s.Status = state.StatusError
			s.Error = err.Error()
			s.PhaseDetail = "配置获取失败"
		})
		return fmt.Errorf("config fetch: %w", err)
	}
	log.Infof("SITE_KEY=%s ACTION_ID=%s… source=%s profile=%s", scfg.SiteKey, trim(scfg.ActionID, 12), scfg.Source, e.xai.Profile())
	log.OKf("注册服务已启动 | 目标 %d | run=%s | impersonate=%s", e.opt.Target, e.opt.Run.RunID, e.xai.Profile())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// signal
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		select {
		case <-sigCh:
			log.Warn("收到停止信号，正在退出...")
			cancel()
		case <-ctx.Done():
		}
	}()

	// status ticker
	e.wgAux.Add(1)
	go func() {
		defer e.wgAux.Done()
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				e.refreshState()
			}
		}
	}()

	for i := 0; i < sWorkers; i++ {
		e.wgReg.Add(1)
		go e.sWorker(ctx, i, scfg)
	}
	for i := 0; i < pWorkers; i++ {
		e.wgReg.Add(1)
		go e.pWorker(ctx, i)
	}
	for i := 0; i < cWorkers; i++ {
		e.wgReg.Add(1)
		go e.cWorker(ctx, i, scfg)
	}
	for i := 0; i < oauthWorkers; i++ {
		e.wgOAuth.Add(1)
		go e.oauthWorker(ctx, i)
	}

	// wait until target or cancel
	for {
		if int(e.done.Load()) >= e.opt.Target {
			log.OKf("已达目标 %d，停止", e.opt.Target)
			cancel()
			break
		}
		select {
		case <-ctx.Done():
			goto shutdown
		case <-time.After(500 * time.Millisecond):
		}
	}
shutdown:
	// 1) stop S/P/C producers (ctx canceled)
	// 2) wait register workers so no more sends to oauthCh
	// 3) close oauthCh so OAuth workers exit range
	// 4) wait CPA management uploads (async; used to be killed on exit)
	waitGroupTimeout(&e.wgReg, 15*time.Second, log, "register workers")
	close(e.oauthCh)
	waitGroupTimeout(&e.wgOAuth, 30*time.Second, log, "oauth workers")
	uploadWait := 90 * time.Second
	if cfg.CPAUploadEnabled {
		// timeout * (retries+1) + verify + margin
		to := cfg.CPAUploadTimeoutSec
		if to <= 0 {
			to = 30
		}
		retries := cfg.CPAUploadRetries
		if retries < 0 {
			retries = 0
		}
		uploadWait = time.Duration(to*(retries+1)+30) * time.Second
		if uploadWait < 60*time.Second {
			uploadWait = 60 * time.Second
		}
		if uploadWait > 5*time.Minute {
			uploadWait = 5 * time.Minute
		}
		log.Infof("[cpa] 等待 Management 上传完成（最多 %s）…", uploadWait)
	}
	waitGroupTimeout(&e.wgUpload, uploadWait, log, "cpa upload")
	waitGroupTimeout(&e.wgAux, 3*time.Second, log, "aux")

	_ = st.Set(func(s *state.Snapshot) {
		if s.Status != state.StatusError {
			s.Status = state.StatusStopped
		}
		s.Phase = state.PhaseIdle
		s.PhaseDetail = fmt.Sprintf("完成 %d/%d", e.done.Load(), e.opt.Target)
		s.Done = int(e.done.Load())
		s.SSOCount = int(e.ssoN.Load())
		s.OAuthCount = int(e.oaN.Load())
		s.FailCount = int(e.fail.Load())
		s.PID = 0
	})
	log.Infof("结束 done=%d sso=%d oauth=%d fail=%d", e.done.Load(), e.ssoN.Load(), e.oaN.Load(), e.fail.Load())
	return nil
}

func (e *Engine) refreshState() {
	elapsed := time.Since(e.start).Minutes()
	rate := 0.0
	if elapsed > 0 {
		rate = float64(e.done.Load()) / elapsed
	}
	t, q := e.inv.Depths()
	_ = e.opt.Store.Set(func(s *state.Snapshot) {
		s.Done = int(e.done.Load())
		s.SSOCount = int(e.ssoN.Load())
		s.OAuthCount = int(e.oaN.Load())
		s.FailCount = int(e.fail.Load())
		s.RatePerMin = rate
		if s.Phase == state.PhaseRegister || s.Phase == "" {
			s.PhaseDetail = fmt.Sprintf("注册中 T=%d Q=%d done=%d/%d inflight=%d", t, q, e.done.Load(), e.opt.Target, e.reserved.Load())
		}
	})
}

func waitGroupTimeout(wg *sync.WaitGroup, d time.Duration, log *logx.Logger, name string) {
	ch := make(chan struct{})
	go func() {
		wg.Wait()
		close(ch)
	}()
	select {
	case <-ch:
	case <-time.After(d):
		log.Warnf("%s 退出超时", name)
	}
}

func (e *Engine) sWorker(ctx context.Context, id int, scfg protocol.SignupConfig) {
	defer e.wgReg.Done()
	log := e.opt.Log
	pageURL := protocol.SiteURL + "/sign-up"
	for {
		if e.remainingCapacity() <= 0 && int(e.done.Load()) >= e.opt.Target {
			return
		}
		// Don't mint far ahead of what we still need.
		if e.remainingCapacity() <= 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
			if int(e.done.Load()) >= e.opt.Target {
				return
			}
			continue
		}
		tDepth, _ := e.inv.Depths()
		need := e.remainingCapacity()
		if need < 1 {
			need = 1
		}
		if tDepth >= need {
			select {
			case <-ctx.Done():
				return
			case <-time.After(400 * time.Millisecond):
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := e.phys.Acquire(ctx); err != nil {
			return
		}
		tok, err := e.turn.Solve(ctx, scfg.SiteKey, pageURL)
		e.phys.Release()
		if err != nil {
			log.Warnf("[S%d] turnstile: %v", id, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if err := e.inv.PutT(ctx, tok, 5*time.Minute); err != nil {
			return
		}
		log.Infof("[S%d] token ok (len=%d)", id, len(tok))
	}
}

func (e *Engine) pWorker(ctx context.Context, id int) {
	defer e.wgReg.Done()
	log := e.opt.Log
	for {
		if int(e.done.Load()) >= e.opt.Target {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		// Global seat: done + reserved <= target (not per-worker).
		if e.remainingCapacity() <= 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
			if int(e.done.Load()) >= e.opt.Target {
				return
			}
			continue
		}
		// Don't flood Q ahead of Turnstile: cap pending Q near S workers.
		_, qDepth := e.inv.Depths()
		qCap := e.opt.Cfg.TurnstileWorkers
		if qCap < 1 {
			qCap = 2
		}
		if qCap > 3 {
			qCap = 3
		}
		if rem := e.remainingCapacity(); rem < qCap {
			qCap = rem
		}
		if qCap < 1 {
			qCap = 1
		}
		if qDepth >= qCap {
			select {
			case <-ctx.Done():
				return
			case <-time.After(800 * time.Millisecond):
			}
			continue
		}

		// Reserve seat BEFORE creating email so multi-P cannot overshoot -t.
		if !e.tryReserve() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
			continue
		}

		if err := e.qPending.Acquire(ctx); err != nil {
			e.releaseReserve()
			return
		}
		h, err := e.mail.Create()
		if err != nil {
			e.qPending.Release()
			e.releaseReserve()
			log.Debugf("[P%d] create email: %v", id, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		if err := e.xai.CreateEmailCode(h.Email); err != nil {
			e.qPending.Release()
			e.releaseReserve()
			log.Debugf("[P%d] create code %s: %v", id, h.Email, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		code, err := e.mail.PollCode(h, 90*time.Second)
		if err != nil {
			e.qPending.Release()
			e.releaseReserve()
			log.Debugf("[P%d] poll code: %v", id, err)
			continue
		}
		item := QItem{Email: h.Email, Password: h.Password, Code: code, Handle: h}
		// Q TTL must outlive slow Turnstile; onExpire frees reserved seat (was leaking → stuck).
		email := h.Email
		if err := e.inv.PutQWithExpire(ctx, item, 8*time.Minute, func() {
			e.releaseReserve()
			log.Warnf("[P%d] Q 过期丢弃 %s（座位已释放 reserved=%d）", id, email, e.reserved.Load())
		}); err != nil {
			e.qPending.Release()
			e.releaseReserve()
			return
		}
		e.qPending.Release()
		// seat stays reserved until signup fail / oauth fail / CPA success / Q TTL expire
		log.Debugf("[P%d] Q ready %s (reserved=%d done=%d/%d)", id, h.Email, e.reserved.Load(), e.done.Load(), e.opt.Target)
	}
}

func (e *Engine) cWorker(ctx context.Context, id int, scfg protocol.SignupConfig) {
	defer e.wgReg.Done()
	log := e.opt.Log
	for {
		if int(e.done.Load()) >= e.opt.Target {
			return
		}
		pair, err := e.inv.ClaimPair(ctx)
		if err != nil {
			return
		}
		token := pair.T.Value
		q := pair.Q.Value
		_ = e.opt.Store.Set(func(s *state.Snapshot) {
			s.Phase = state.PhaseRegister
			s.PhaseDetail = fmt.Sprintf("正在注册 %s", q.Email)
		})
		log.Startf("开始注册 %s", q.Email)

		e.xai.ClearAuthCookies()
		if err := e.xai.VerifyEmailCode(q.Email, q.Code); err != nil {
			log.Warnf("verify fail %s code=%s: %v", q.Email, protocol.CodeOf(err), err)
			pair.Release()
			e.fail.Add(1)
			e.releaseReserve()
			continue
		}
		// Optional ValidatePassword (document field 4/5); non-fatal
		if err := e.xai.ValidatePassword(q.Email, q.Password); err != nil {
			log.Debugf("validate_password skip/fail %s: %v", q.Email, err)
		}
		body := protocol.BuildSignupBody(q.Email, q.Password, q.Code, token)
		text, sso, err := e.xai.SignupServerAction(body, scfg.ActionID, scfg.StateTree)
		if sso == "" {
			sso = protocol.ExtractSSOFromText(text)
		}
		pair.Release()
		if err != nil || sso == "" {
			preview := text
			if len(preview) > 180 {
				preview = preview[:180]
			}
			log.Warnf("signup fail %s code=%s err=%v sso=%v body=%q", q.Email, protocol.CodeOf(err), err, sso != "", preview)
			e.fail.Add(1)
			e.releaseReserve() // free seat for another attempt
			continue
		}

		// ensure run dirs exist (first credential)
		accPath := filepath.Join(e.opt.Run.SSO, "accounts.txt")
		if err := cpa.AppendSSO(accPath, q.Email, q.Password, sso); err != nil {
			log.Warnf("write sso: %v", err)
		}
		_ = cpa.AppendAuthSession(filepath.Join(e.opt.Run.SSO, "auth-sessions.jsonl"), q.Email, sso)
		// grok2api: bare SSO token only (one per line)
		if e.opt.Run.Grok2API != "" {
			if err := cpa.AppendGrok2APIToken(e.opt.Run.Grok2API, sso); err != nil {
				log.Warnf("write grok2api token: %v", err)
			}
		}
		n := e.ssoN.Add(1)
		log.OKf("注册成功 #%d %s", n, q.Email)
		// Brief settle: brand-new SSO sometimes rejected by auth.x.ai device verify (→ sign-in / invalid_grant).
		select {
		case <-ctx.Done():
			e.releaseReserve()
			return
		case <-time.After(2 * time.Second):
		}

		job := SSOJob{Email: q.Email, Password: q.Password, SSO: sso}
		select {
		case <-ctx.Done():
			e.releaseReserve()
			return
		case e.oauthCh <- job:
		default:
			select {
			case <-ctx.Done():
				e.releaseReserve()
				return
			case e.oauthCh <- job:
			}
		}
	}
}

func (e *Engine) waitOAuthSlot(ctx context.Context) error {
	minInterval := time.Duration(e.opt.Cfg.OAuthMinIntervalSec * float64(time.Second))
	if minInterval < 0 {
		minInterval = 0
	}
	for {
		e.oauthGateMu.Lock()
		wait := time.Duration(0)
		if !e.oauthLastStart.IsZero() && minInterval > 0 {
			wait = time.Until(e.oauthLastStart.Add(minInterval))
		}
		if wait <= 0 {
			e.oauthLastStart = time.Now()
			e.oauthGateMu.Unlock()
			return nil
		}
		e.oauthGateMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

func (e *Engine) oauthWorker(ctx context.Context, id int) {
	defer e.wgOAuth.Done()
	log := e.opt.Log
	for job := range e.oauthCh {
		// Soft-stop: still drain with seat accounting, but skip work past target.
		if int(e.done.Load()) >= e.opt.Target {
			e.releaseReserve()
			continue
		}
		if err := e.waitOAuthSlot(ctx); err != nil {
			return
		}
		_ = e.opt.Store.Set(func(s *state.Snapshot) {
			s.Phase = state.PhaseOAuth
			s.PhaseDetail = fmt.Sprintf("正在 OAuth (%s)", job.Email)
		})
		log.Startf("OAuth %s", job.Email)
		t0 := time.Now()
		// SSO preview for debugging (prefix only)
		ssoPrev := job.SSO
		if len(ssoPrev) > 24 {
			ssoPrev = ssoPrev[:12] + "…" + ssoPrev[len(ssoPrev)-8:]
		}
		cred, err := e.oauth.Exchange(ctx, job.SSO)
		if err != nil {
			log.Warnf("OAuth fail %s: %v (%.1fs) sso=%s", job.Email, err, time.Since(t0).Seconds(), ssoPrev)
			e.fail.Add(1)
			e.releaseReserve()
			continue
		}
		log.Infof("OAuth ok %s (%.1fs)", job.Email, time.Since(t0).Seconds())
		e.oaN.Add(1)
		doc := cpa.FromCredential(cred, job.Email)
		_ = e.opt.Store.Set(func(s *state.Snapshot) {
			s.Phase = state.PhaseProbe
			s.PhaseDetail = fmt.Sprintf("探活 %s", job.Email)
		})
		if e.opt.Cfg.ProbeEnabled {
			warmup := e.opt.Cfg.ProbeWarmupSec
			if err := cpa.Probe(doc, e.opt.Cfg.RegisterProxy, warmup); err != nil {
				log.Warnf("探活失败 %s: %v", job.Email, err)
				path, _ := cpa.WriteAtomic(e.opt.Run.Discarded, doc, cpa.DefaultSecret())
				_ = path
				e.fail.Add(1)
				e.releaseReserve()
				continue
			}
		}
		// Atomic complete: prevents multi-OAuth overshoot of -t.
		d, ok := e.tryComplete()
		if !ok {
			// Target already filled by another worker — keep file in discarded.
			path, _ := cpa.WriteAtomic(e.opt.Run.Discarded, doc, cpa.DefaultSecret())
			log.Warnf("已达目标，额外号移入 discarded: %s (%s)", job.Email, filepath.Base(path))
			continue
		}
		path, err := cpa.WriteAtomic(e.opt.Run.CPA, doc, cpa.DefaultSecret())
		if err != nil {
			log.Warnf("写 CPA 失败: %v", err)
			// seat already converted to done; count as fail but don't re-open flood
			e.fail.Add(1)
			continue
		}
		if e.uploader != nil && e.uploader.Enabled() {
			up := e.uploader
			docCopy := doc
			e.wgUpload.Add(1)
			go func() {
				defer e.wgUpload.Done()
				defer func() { _ = recover() }()
				log.Infof("[cpa] 开始上传 %s …", docCopy.Email)
				res := up.UploadDocument(docCopy)
				if res.Err != nil {
					log.Warnf("[cpa] 上传失败 %s: %v", docCopy.Email, res.Err)
				} else if !res.OK {
					log.Warnf("[cpa] 上传失败 %s status=%d body=%s", docCopy.Email, res.Status, truncateRunes(res.Body, 180))
				} else if res.Verified {
					log.OKf("[cpa] 已入库 %s → %s", docCopy.Email, res.Name)
				} else {
					log.OKf("[cpa] 已上传 %s → %s（列表校验未命中，可能仍成功）", docCopy.Email, res.Name)
				}
			}()
		}
		log.OKf("CPA 就绪 #%d/%d %s -> %s", d, e.opt.Target, job.Email, filepath.Base(path))
		e.refreshState()
	}
}

func truncateRunes(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func deriveWorkers(cfg config.Config) (s, p, c, oa, phys int) {
	phys = cfg.PhysicalCap
	if phys <= 0 {
		cpus := runtime.NumCPU()
		phys = cpus
		if phys > 4 {
			phys = 4
		}
		if phys < 2 {
			phys = 2
		}
	}
	// Browser Turnstile: parallel slots from runtime --thread (not config.env).
	prov := strings.ToLower(strings.TrimSpace(cfg.TurnstileProvider))
	if prov == "" || prov == "browser" || prov == "local" || prov == "playwright" || prov == "pool" {
		s = cfg.TurnstileWorkers
		if s <= 0 {
			s = 2
		}
		if s > 8 {
			s = 8
		}
		if s < 1 {
			s = 1
		}
		// phys caps concurrent browser mints (= pool slots)
		if cfg.PhysicalCap > 0 && cfg.PhysicalCap < s {
			s = cfg.PhysicalCap
		}
		phys = s
	} else {
		s = phys
		if cfg.TurnstileWorkers > 0 {
			s = cfg.TurnstileWorkers
		}
	}
	// P workers: track --thread (S). Fixed P=4 with S=3 flooded reserved queue.
	target := cfg.Target
	if target <= 0 {
		target = 10
	}
	if s < 1 {
		s = 1
	}
	p = s
	if p > 4 {
		p = 4
	}
	if p > target {
		p = target
	}
	if p < 1 {
		p = 1
	}
	c = 2
	if target < 2 || s < 2 {
		c = 1
	}
	// OAuth: default 1 when register concurrency ≥3 (device/auth 429); else 2.
	oa = cfg.OAuthWorkers
	if oa <= 0 {
		if s >= 3 || target >= 8 {
			oa = 1
		} else {
			oa = 2
		}
	}
	if oa > 4 {
		oa = 4
	}
	if oa < 1 {
		oa = 1
	}
	return
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
