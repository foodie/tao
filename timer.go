package tao

import (
	"container/heap"
	"context"
	"sync"
	"time"

	"github.com/leesper/holmes"
)

const (
	tickPeriod time.Duration = 500 * time.Millisecond
	bufferSize               = 1024
)

//时间轮的id
var timerIds *AtomicInt64

func init() {
	timerIds = NewAtomicInt64(0)
}

//构建一个timerType的slice的堆
//定义一个timeType的数组
// timerHeap is a heap-based priority queue
type timerHeapType []*timerType
//获取index，是堆默认的？
func (heap timerHeapType) getIndexByID(id int64) int {
	for _, t := range heap {
		if t.id == id {
			return t.index
		}
	}
	return -1
}

func (heap timerHeapType) Len() int {
	return len(heap)
}

func (heap timerHeapType) Less(i, j int) bool {
	return heap[i].expiration.UnixNano() < heap[j].expiration.UnixNano()
}

func (heap timerHeapType) Swap(i, j int) {
	heap[i], heap[j] = heap[j], heap[i]
	heap[i].index = i
	heap[j].index = j
}

func (heap *timerHeapType) Push(x interface{}) {
	n := len(*heap)
	timer := x.(*timerType)
	timer.index = n
	*heap = append(*heap, timer)
}

func (heap *timerHeapType) Pop() interface{} {
	old := *heap
	n := len(old)
	timer := old[n-1]
	timer.index = -1
	*heap = old[0 : n-1]
	return timer
}

//基本的时间设置
/* 'expiration' is the time when timer time out, if 'interval' > 0
the timer will time out periodically, 'timeout' contains the callback
to be called when times out */
type timerType struct {
	id         int64//当前的id
	expiration time.Time//过期时间
	interval   time.Duration//间隔执行时间
	timeout    *OnTimeOut//回调函数
	index      int // for container/heap
}

func newTimer(when time.Time, interv time.Duration, to *OnTimeOut) *timerType {
	return &timerType{
		id:         timerIds.GetAndIncrement(),
		expiration: when,
		interval:   interv,
		timeout:    to,
	}
}

//是否是重复执行的
func (t *timerType) isRepeat() bool {
	return int64(t.interval) > 0
}

//时间轮，管理所有的时间任务
// TimingWheel manages all the timed task.
type TimingWheel struct {
	timeOutChan chan *OnTimeOut//超时chan
	timers      timerHeapType//时间轮
	ticker      *time.Ticker//ticker
	wg          *sync.WaitGroup//waitGroup数据
	addChan     chan *timerType // add timer in loop//添加数据的chan
	cancelChan  chan int64      // cancel timer in loop//取消的chan
	sizeChan    chan int        // get size in loop//大小
	ctx         context.Context//上下文
	cancel      context.CancelFunc//取消的函数
}

//对一个上下文建立一个chan
// NewTimingWheel returns a *TimingWheel ready for use.
func NewTimingWheel(ctx context.Context) *TimingWheel {
	timingWheel := &TimingWheel{
		timeOutChan: make(chan *OnTimeOut, bufferSize),
		timers:      make(timerHeapType, 0),
		ticker:      time.NewTicker(tickPeriod),
		wg:          &sync.WaitGroup{},
		addChan:     make(chan *timerType, bufferSize),
		cancelChan:  make(chan int64, bufferSize),
		sizeChan:    make(chan int),
	}
	timingWheel.ctx, timingWheel.cancel = context.WithCancel(ctx)
	heap.Init(&timingWheel.timers)
	timingWheel.wg.Add(1)
	go func() {
		timingWheel.start()
		timingWheel.wg.Done()
	}()
	return timingWheel
}

// TimeOutChannel returns the timeout channel.
func (tw *TimingWheel) TimeOutChannel() chan *OnTimeOut {
	return tw.timeOutChan
}

//添加数据
// AddTimer adds new timed task.
func (tw *TimingWheel) AddTimer(when time.Time, interv time.Duration, to *OnTimeOut) int64 {
	if to == nil {
		return int64(-1)
	}
	timer := newTimer(when, interv, to)
	tw.addChan <- timer
	return timer.id
}

// Size returns the number of timed tasks.
func (tw *TimingWheel) Size() int {
	return <-tw.sizeChan
}

//取消定时器
// CancelTimer cancels a timed task with specified timer ID.
func (tw *TimingWheel) CancelTimer(timerID int64) {
	tw.cancelChan <- timerID
}

// Stop stops the TimingWheel.
func (tw *TimingWheel) Stop() {
	tw.cancel()
	tw.wg.Wait()
}

func (tw *TimingWheel) getExpired() []*timerType {
	expired := make([]*timerType, 0)
	for tw.timers.Len() > 0 {
		timer := heap.Pop(&tw.timers).(*timerType)
		//返回从t到现在经过的时间, 当前时间-过期时间
		elapsed := time.Since(timer.expiration).Seconds()
		//正数表示过期，负数表示未过期
		if elapsed > 1.0 {
			holmes.Warnf("elapsed %f\n", elapsed)
		}
		if elapsed > 0.0 {//已经过期
			expired = append(expired, timer)
			continue
		} else {//如果未过期，则跳出循环
			heap.Push(&tw.timers, timer)
			break
		}
	}
	return expired
}

func (tw *TimingWheel) update(timers []*timerType) {
	if timers != nil {
		for _, t := range timers {
			if t.isRepeat() { // repeatable timer task
				//加上当前时间
				t.expiration = t.expiration.Add(t.interval)
				// if task time out for at least 10 seconds, the expiration time needs
				// to be updated in case this task executes every time timer wakes up.
				//如果还是过期，改为现在
				if time.Since(t.expiration).Seconds() >= 10.0 {
					t.expiration = time.Now()
				}
				//继续放入
				heap.Push(&tw.timers, t)
			}
		}
	}
}

func (tw *TimingWheel) start() {
	for {
		select {
		case timerID := <-tw.cancelChan://删除对应的数据
			index := tw.timers.getIndexByID(timerID)
			if index >= 0 {
				heap.Remove(&tw.timers, index)
			}

		case tw.sizeChan <- tw.timers.Len()://获取数据的长度

		case <-tw.ctx.Done()://完成tick
			tw.ticker.Stop()
			return

		case timer := <-tw.addChan://放入数据
			heap.Push(&tw.timers, timer)

		case <-tw.ticker.C:
			timers := tw.getExpired()//获取过期的数据
			for _, t := range timers {
				//调用chan过期的
				tw.TimeOutChannel() <- t.timeout
			}
			tw.update(timers)//更新时间
		}
	}
}
