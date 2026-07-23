package inventory

import (
	"context"
	"testing"
	"time"
)

func TestClaimPair(t *testing.T) {
	inv := New[string, int](4, 4)
	ctx := context.Background()
	if err := inv.PutT(ctx, "tok", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := inv.PutQ(ctx, 42, time.Minute); err != nil {
		t.Fatal(err)
	}
	pair, err := inv.ClaimPair(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pair.T.Value != "tok" || pair.Q.Value != 42 {
		t.Fatalf("unexpected pair %+v", pair)
	}
	pair.Release()
	tt, qq := inv.Depths()
	if tt != 0 || qq != 0 {
		t.Fatalf("depths t=%d q=%d", tt, qq)
	}
}

func TestSemaphore(t *testing.T) {
	s := NewSemaphore(1)
	ctx := context.Background()
	if err := s.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := s.Acquire(ctx2); err == nil {
		t.Fatal("expected timeout")
	}
	s.Release()
}

func TestQExpireOnExpire(t *testing.T) {
	inv := New[string, int](2, 2)
	ctx := context.Background()
	var n int
	if err := inv.PutQWithExpire(ctx, 7, 30*time.Millisecond, func() { n++ }); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	tt, qq := inv.Depths()
	if tt != 0 || qq != 0 {
		t.Fatalf("expected empty after expire, t=%d q=%d", tt, qq)
	}
	if n != 1 {
		t.Fatalf("onExpire called %d want 1", n)
	}
}
