package inventory

import (
	"context"
	"sync"
	"time"
)

type Envelope[T any] struct {
	Value     T
	CreatedAt time.Time
	ExpiresAt time.Time
	release   func() // always: free inventory slot
	onExpire  func() // optional: only when TTL purge discards item
	once      sync.Once
}

func (e *Envelope[T]) Release() {
	if e == nil {
		return
	}
	e.once.Do(func() {
		if e.release != nil {
			e.release()
		}
	})
}

// expire drops a TTL'd item: slot + onExpire (e.g. pipeline seat).
func (e *Envelope[T]) expire() {
	if e == nil {
		return
	}
	e.once.Do(func() {
		if e.onExpire != nil {
			e.onExpire()
		}
		if e.release != nil {
			e.release()
		}
	})
}

func (e *Envelope[T]) Expired() bool {
	if e.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(e.ExpiresAt)
}

type Pair[T, Q any] struct {
	T *Envelope[T]
	Q *Envelope[Q]
}

func (p *Pair[T, Q]) Release() {
	if p == nil {
		return
	}
	p.T.Release()
	p.Q.Release()
}

// Inventory is a CSP-style T/Q store with atomic claim_pair.
type Inventory[T, Q any] struct {
	mu     sync.Mutex
	waitCh chan struct{}
	ts     []*Envelope[T]
	qs     []*Envelope[Q]

	tSlots *Semaphore
	qSlots *Semaphore
}

func New[T, Q any](tCap, qCap int) *Inventory[T, Q] {
	return &Inventory[T, Q]{
		waitCh: make(chan struct{}, 1),
		tSlots: NewSemaphore(tCap),
		qSlots: NewSemaphore(qCap),
	}
}

func (inv *Inventory[T, Q]) signal() {
	select {
	case inv.waitCh <- struct{}{}:
	default:
	}
}

func (inv *Inventory[T, Q]) PutT(ctx context.Context, v T, maxAge time.Duration) error {
	return inv.PutTWithExpire(ctx, v, maxAge, nil)
}

// PutTWithExpire is PutT plus optional onExpire (TTL discard only).
func (inv *Inventory[T, Q]) PutTWithExpire(ctx context.Context, v T, maxAge time.Duration, onExpire func()) error {
	if err := inv.tSlots.Acquire(ctx); err != nil {
		return err
	}
	env := &Envelope[T]{
		Value:     v,
		CreatedAt: time.Now(),
		release:   func() { inv.tSlots.Release() },
		onExpire:  onExpire,
	}
	if maxAge > 0 {
		env.ExpiresAt = time.Now().Add(maxAge)
	}
	inv.mu.Lock()
	inv.purgeLocked()
	inv.ts = append(inv.ts, env)
	inv.mu.Unlock()
	inv.signal()
	return nil
}

func (inv *Inventory[T, Q]) PutQ(ctx context.Context, v Q, maxAge time.Duration) error {
	return inv.PutQWithExpire(ctx, v, maxAge, nil)
}

// PutQWithExpire is PutQ plus optional onExpire (TTL discard only; use to free pipeline seats).
func (inv *Inventory[T, Q]) PutQWithExpire(ctx context.Context, v Q, maxAge time.Duration, onExpire func()) error {
	if err := inv.qSlots.Acquire(ctx); err != nil {
		return err
	}
	env := &Envelope[Q]{
		Value:     v,
		CreatedAt: time.Now(),
		release:   func() { inv.qSlots.Release() },
		onExpire:  onExpire,
	}
	if maxAge > 0 {
		env.ExpiresAt = time.Now().Add(maxAge)
	}
	inv.mu.Lock()
	inv.purgeLocked()
	inv.qs = append(inv.qs, env)
	inv.mu.Unlock()
	inv.signal()
	return nil
}

func (inv *Inventory[T, Q]) ClaimPair(ctx context.Context) (*Pair[T, Q], error) {
	for {
		inv.mu.Lock()
		inv.purgeLocked()
		if len(inv.ts) > 0 && len(inv.qs) > 0 {
			t := inv.ts[0]
			q := inv.qs[0]
			inv.ts = inv.ts[1:]
			inv.qs = inv.qs[1:]
			inv.mu.Unlock()
			return &Pair[T, Q]{T: t, Q: q}, nil
		}
		inv.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-inv.waitCh:
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (inv *Inventory[T, Q]) Depths() (t, q int) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	inv.purgeLocked()
	return len(inv.ts), len(inv.qs)
}

func (inv *Inventory[T, Q]) purgeLocked() {
	now := time.Now()
	var ts []*Envelope[T]
	for _, e := range inv.ts {
		if !e.ExpiresAt.IsZero() && now.After(e.ExpiresAt) {
			e.expire() // slot + onExpire
			continue
		}
		ts = append(ts, e)
	}
	inv.ts = ts
	var qs []*Envelope[Q]
	for _, e := range inv.qs {
		if !e.ExpiresAt.IsZero() && now.After(e.ExpiresAt) {
			e.expire()
			continue
		}
		qs = append(qs, e)
	}
	inv.qs = qs
}

// Semaphore is a counting semaphore with context.
type Semaphore struct {
	ch chan struct{}
}

func NewSemaphore(n int) *Semaphore {
	if n < 1 {
		n = 1
	}
	s := &Semaphore{ch: make(chan struct{}, n)}
	for i := 0; i < n; i++ {
		s.ch <- struct{}{}
	}
	return s
}

func (s *Semaphore) Acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ch:
		return nil
	}
}

func (s *Semaphore) Release() {
	select {
	case s.ch <- struct{}{}:
	default:
	}
}

func (s *Semaphore) Cap() int { return cap(s.ch) }
