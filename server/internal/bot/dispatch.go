// Dispatcher 把收到的消息转成异步任务。
//
// 为什么必须有它：CSV 导入（下载+解析+入库+上百人重算）动辄几十秒，
// 而两个通道的收消息循环（QQ 的 WS 读循环、微信的长轮询）原来都是
// 同步调 Handle——一个 CSV 在处理，第二个 CSV 就得干等，群里看起来
// 就是机器人卡死了。
//
// 语义：
//   - 同一会话严格串行：日期口令、改名/改号多步对话、回复顺序都不能乱
//   - 不同会话并行，但全局并发由信号量限制，防止一波导入把连接池打爆
//   - 每个任务带超时，真卡住也不会永远占着队列
package bot

import (
	"context"
	"sync"
	"time"
)

type Dispatcher struct {
	root    context.Context
	sem     chan struct{}
	mu      sync.Mutex
	queues  map[string]chan func(context.Context)
	timeout time.Duration
}

// NewDispatcher maxConcurrent 是全局同时处理的消息数上限；
// perTaskTimeout 是单条消息的处理上限（超时只取消该任务，不影响通道）。
func NewDispatcher(root context.Context, maxConcurrent int, perTaskTimeout time.Duration) *Dispatcher {
	if maxConcurrent < 1 {
		maxConcurrent = 2
	}
	if perTaskTimeout <= 0 {
		perTaskTimeout = 5 * time.Minute
	}
	return &Dispatcher{
		root:    root,
		sem:     make(chan struct{}, maxConcurrent),
		queues:  map[string]chan func(context.Context){},
		timeout: perTaskTimeout,
	}
}

// Submit 投递一条消息。同一会话按投递顺序执行；队列满时阻塞调用方
// （收消息循环），这是刻意的背压——宁可慢也不能丢消息。
func (d *Dispatcher) Submit(conversationID string, fn func(ctx context.Context)) {
	d.mu.Lock()
	q, ok := d.queues[conversationID]
	if !ok {
		q = make(chan func(context.Context), 64)
		d.queues[conversationID] = q
		go d.run(q)
	}
	d.mu.Unlock()
	q <- fn
}

func (d *Dispatcher) run(q chan func(context.Context)) {
	for fn := range q {
		d.sem <- struct{}{}
		ctx, cancel := context.WithTimeout(d.root, d.timeout)
		func() {
			defer cancel()
			fn(ctx)
		}()
		<-d.sem
	}
}
