package bot

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 同会话必须严格按投递顺序执行——日期口令、多步对话全靠这个顺序。
func TestDispatcherSameConversationOrdered(t *testing.T) {
	d := NewDispatcher(context.Background(), 4, time.Minute)
	var order []int
	var mu sync.Mutex
	done := make(chan struct{})

	for i := 1; i <= 5; i++ {
		i := i
		d.Submit("conv-1", func(ctx context.Context) {
			time.Sleep(10 * time.Millisecond) // 制造乱序机会
			mu.Lock()
			order = append(order, i)
			if len(order) == 5 {
				close(done)
			}
			mu.Unlock()
		})
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("任务没有全部执行")
	}
	for i := 0; i < 5; i++ {
		if order[i] != i+1 {
			t.Fatalf("顺序错了: %v", order)
		}
	}
}

// 不同会话并行；全局并发受信号量限制。
func TestDispatcherCrossConversationConcurrent(t *testing.T) {
	d := NewDispatcher(context.Background(), 2, time.Minute)
	var running, maxRunning int32
	var wg sync.WaitGroup

	for i := 0; i < 6; i++ {
		wg.Add(1)
		d.Submit("conv-"+string(rune('a'+i)), func(ctx context.Context) {
			defer wg.Done()
			cur := atomic.AddInt32(&running, 1)
			for {
				old := atomic.LoadInt32(&maxRunning)
				if cur <= old || atomic.CompareAndSwapInt32(&maxRunning, old, cur) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			atomic.AddInt32(&running, -1)
		})
	}
	wg.Wait()

	if maxRunning > 2 {
		t.Fatalf("并发超限: %d", maxRunning)
	}
	if maxRunning < 2 {
		t.Fatalf("没有并行起来: %d", maxRunning)
	}
}

// 单任务超时只取消它自己，不能把队列或别的会话拖死。
func TestDispatcherTaskTimeout(t *testing.T) {
	d := NewDispatcher(context.Background(), 2, 50*time.Millisecond)
	blocked := make(chan struct{})
	released := make(chan struct{})

	d.Submit("slow", func(ctx context.Context) {
		close(blocked)
		select {
		case <-ctx.Done():
			close(released)
		case <-time.After(5 * time.Second):
		}
	})
	<-blocked
	<-released // 超时生效

	// 队列还能继续干活
	ok := make(chan struct{})
	d.Submit("slow", func(ctx context.Context) { close(ok) })
	select {
	case <-ok:
	case <-time.After(5 * time.Second):
		t.Fatal("超时后队列卡死")
	}
}
