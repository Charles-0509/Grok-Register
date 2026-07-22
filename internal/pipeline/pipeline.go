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

	start   time.Time
	wgReg   sync.WaitGroup // S/P/C
	wgOAuth sync.WaitGroup
	wgAux   sync.WaitGroup // status ticker etc
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
	// Pending email codes in flight: cap by target so target=5 doesn't open 12 boxes.
	qPend := cfg.Target
	if qPend <= 0 {
		qPend = 4
	}
	if qPend > 6 {
		qPend = 6
	}
	if qPend < 2 {
		qPend = 2
	}
	e.qPending = inventory.NewSemaphore(qPend)
	tSlots, qSlots := 4, 4
	if cfg.Target > 0 && cfg.Target < 4 {
		tSlots, qSlots = cfg.Target, cfg.Target
	}
	e.inv = inventory.New[string, QItem](tSlots, qSlots)
	log.Infof("workers S=%d P=%d C=%d OAuth=%d phys=%d q_pending=%d", sWorkers, pWorkers, cWorkers, oauthWorkers, physCap, qPend)

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

	// Clearance
	if cfg.ClearanceEnabled {
		e.cm = clearance.NewManager(cfg.FlareSolverrURL, cfg.ClearanceProxy, cfg.ClearanceURLs)
		msg, err := e.cm.Prewarm()
		if err != nil {
			log.Warnf("clearance: %v (%s)", err, msg)
		} else {
			log.Infof("[clearance] %s", msg)
		}
	} else {
		log.Info("[clearance] 未启用")
	}

	var err error
	e.xai, err = protocol.NewClient(cfg.RegisterProxy, e.cm)
	if err != nil {
		return err
	}
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
	e.turn = turnstile.New(turnstile.Options{
		Provider: cfg.TurnstileProvider,
		LiteURL:  cfg.LiteSolverURL,
		Proxy:    cfg.RegisterProxy,
		Clear:    e.cm,
		Workers:  sWorkers, // parallel S = pool slots
	})
	if c, ok := e.turn.(turnstile.Closer); ok {
		defer c.Close()
	}
	log.Infof("Turnstile provider=%s workers=%d (pool → one-shot mint → chromedp)", e.turn.Name(), sWorkers)
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
	log.Info("Fetching signup config...")
	scfg, err := e.xai.FetchConfig()
	if err != nil {
		_ = st.Set(func(s *state.Snapshot) {
			s.Status = state.StatusError
			s.Error = err.Error()
			s.PhaseDetail = "配置获取失败"
		})
		return fmt.Errorf("config fetch: %w", err)
	}
	log.Infof("SITE_KEY=%s ACTION_ID=%s...", scfg.SiteKey, trim(scfg.ActionID, 12))
	log.OKf("注册服务已启动 | 目标 %d | run=%s", e.opt.Target, e.opt.Run.RunID)

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
	waitGroupTimeout(&e.wgReg, 15*time.Second, log, "register workers")
	close(e.oauthCh)
	waitGroupTimeout(&e.wgOAuth, 30*time.Second, log, "oauth workers")
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
		_, qDepth := e.inv.Depths()
		qCap := e.remainingCapacity()
		if qCap > 4 {
			qCap = 4
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
		if err := e.inv.PutQ(ctx, item, 2*time.Minute); err != nil {
			e.qPending.Release()
			e.releaseReserve()
			return
		}
		e.qPending.Release()
		// seat stays reserved until signup fail / oauth fail / CPA success
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
			log.Warnf("verify fail %s: %v", q.Email, err)
			pair.Release()
			e.fail.Add(1)
			e.releaseReserve()
			continue
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
			log.Warnf("signup fail %s: err=%v sso=%v body=%q", q.Email, err, sso != "", preview)
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
		n := e.ssoN.Add(1)
		log.OKf("注册成功 #%d %s", n, q.Email)

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

func (e *Engine) oauthWorker(ctx context.Context, id int) {
	defer e.wgOAuth.Done()
	log := e.opt.Log
	minInterval := time.Duration(e.opt.Cfg.OAuthMinIntervalSec * float64(time.Second))
	if minInterval <= 0 {
		minInterval = 10 * time.Second
	}
	var last time.Time
	for job := range e.oauthCh {
		// Soft-stop: still drain with seat accounting, but skip work past target.
		if int(e.done.Load()) >= e.opt.Target {
			e.releaseReserve()
			continue
		}
		if !last.IsZero() {
			if d := time.Until(last.Add(minInterval)); d > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(d):
				}
			}
		}
		last = time.Now()
		_ = e.opt.Store.Set(func(s *state.Snapshot) {
			s.Phase = state.PhaseOAuth
			s.PhaseDetail = fmt.Sprintf("正在 OAuth (%s)", job.Email)
		})
		log.Startf("OAuth %s", job.Email)
		cred, err := e.oauth.Exchange(ctx, job.SSO)
		if err != nil {
			log.Warnf("OAuth fail %s: %v", job.Email, err)
			e.fail.Add(1)
			e.releaseReserve()
			continue
		}
		e.oaN.Add(1)
		doc := cpa.FromCredential(cred, job.Email)
		_ = e.opt.Store.Set(func(s *state.Snapshot) {
			s.Phase = state.PhaseProbe
			s.PhaseDetail = fmt.Sprintf("探活 %s", job.Email)
		})
		if e.opt.Cfg.ProbeEnabled {
			if err := cpa.Probe(doc, e.opt.Cfg.RegisterProxy); err != nil {
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
			go func() {
				defer func() { _ = recover() }()
				_ = up.UploadDocument(docCopy)
			}()
		}
		log.OKf("CPA 就绪 #%d/%d %s -> %s", d, e.opt.Target, job.Email, filepath.Base(path))
		e.refreshState()
	}
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
	// P workers: don't spawn 8 when target is 5 (was flooding tempmail).
	target := cfg.Target
	if target <= 0 {
		target = 10
	}
	p = target
	if p > 4 {
		p = 4
	}
	if p < 1 {
		p = 1
	}
	c = 2
	if target < 2 {
		c = 1
	}
	oa = 2
	if s < 1 {
		s = 1
	}
	return
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
