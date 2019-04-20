package tao

import (
	"container/heap"
	"context"
	"sync"
	"time"

	"github.com/leesper/holmes"
)

//设置时间和buf大小
const (
	tickPeriod time.Duration = 500 * time.Millisecond
	bufferSize               = 1024
)

//时间轮的id
var timerIds *AtomicInt64

func init() {
	timerIds = NewAtomicInt64(0)
}

//根据id获取对应的索引值
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

//返回长度
func (heap timerHeapType) Len() int {
	return len(heap)
}

//比较时间大小
func (heap timerHeapType) Less(i, j int) bool {
	return heap[i].expiration.UnixNano() < heap[j].expiration.UnixNano()
}

//交互数据
func (heap timerHeapType) Swap(i, j int) {
	heap[i], heap[j] = heap[j], heap[i]
	heap[i].index = i
	heap[j].index = j
}

//放置数据
func (heap *timerHeapType) Push(x interface{}) {
	n := len(*heap)
	timer := x.(*timerType)
	timer.index = n
	*heap = append(*heap, timer)
}

//弹出数据
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
//定义一个时间类型
type timerType struct {
	id         int64         //id
	expiration time.Time     //从什么时间开始执行
	interval   time.Duration //间隔时间
	timeout    *OnTimeOut    //超时执行
	index      int           // for container/heap//heap里面的index
}

//新建一个timertyper
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

// TimingWheel manages all the timed task.
type TimingWheel struct {
	timeOutChan chan *OnTimeOut    //超时执行的chan
	timers      timerHeapType      //定义堆
	ticker      *time.Ticker       //ticker
	wg          *sync.WaitGroup    //等待
	addChan     chan *timerType    // add timer in loop //添加时间的chanchan
	cancelChan  chan int64         // cancel timer in loop//取消时间的chan
	sizeChan    chan int           // get size in loop//大小的chan
	ctx         context.Context    //shagnxiaowshagnxiaow//上下文
	cancel      context.CancelFunc //取消时调用的函数
}



//对一个上下文建立一个chan
// NewTimingWheel returns a *TimingWheel ready for use.
//新建一个实践论
func NewTimingWheel(ctx context.Context) *TimingWheel {
	timingWheel := &TimingWheel{
		timeOutChan: make(chan *OnTimeOut, bufferSize), //超时的处理
		timers:      make(timerHeapType, 0),            //构建堆
		ticker:      time.NewTicker(tickPeriod),        //初始化ticker
		wg:          &sync.WaitGroup{},                 //初始化wait
		addChan:     make(chan *timerType, bufferSize), //添加的chan
		cancelChan:  make(chan int64, bufferSize),      //取消的chan
		sizeChan:    make(chan int),                    //大熊啊
	}
	//构建取消chan
	timingWheel.ctx, timingWheel.cancel = context.WithCancel(ctx)
	//初始化堆
	heap.Init(&timingWheel.timers)
	timingWheel.wg.Add(1)
	go func() {
		timingWheel.start()
		//结束
		timingWheel.wg.Done()
	}()
	//返回一个时间轮
	return timingWheel
}

//返回OnTimeOut
// TimeOutChannel returns the timeout channel.
func (tw *TimingWheel) TimeOutChannel() chan *OnTimeOut {
	return tw.timeOutChan
}

//增加一个新的时间

// AddTimer adds new timed task.
func (tw *TimingWheel) AddTimer(when time.Time, interv time.Duration, to *OnTimeOut) int64 {
	if to == nil {
		return int64(-1)
	}
	timer := newTimer(when, interv, to)
	tw.addChan <- timer
	return timer.id
}

//返回大小
// Size returns the number of timed tasks.
func (tw *TimingWheel) Size() int {
	return <-tw.sizeChan
}

//取消一个id的timer
// CancelTimer cancels a timed task with specified timer ID.
func (tw *TimingWheel) CancelTimer(timerID int64) {
	tw.cancelChan <- timerID
}

//暂停时间轮
// Stop stops the TimingWheel.
func (tw *TimingWheel) Stop() {
	tw.cancel()
	tw.wg.Wait()
}

//获取过期的时间
func (tw *TimingWheel) getExpired() []*timerType {
	expired := make([]*timerType, 0)
	//如果存在时间
	for tw.timers.Len() > 0 {
		//弹出一个时间
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

//批量更新时间轮
func (tw *TimingWheel) update(timers []*timerType) {
	if timers != nil {
		for _, t := range timers {
			if t.isRepeat() { // repeatable timer task
				//下个周期继续执行
				t.expiration = t.expiration.Add(t.interval)
				// if task time out for at least 10 seconds,
				//the expiration time needs
				// to be updated in case this task
				//executes every time timer wakes up.
				//如果过期时间大于10s，设置时间为当前时间，保证它立马执行
				if time.Since(t.expiration).Seconds() >= 10.0 {
					t.expiration = time.Now()
				}
				//重新放入时间轮
				heap.Push(&tw.timers, t)
			}
		}
	}
}

func (tw *TimingWheel) start() {
	//死循环
	for {
		select {
		case timerID := <-tw.cancelChan: //按照id取消时间轮

			index := tw.timers.getIndexByID(timerID)
			if index >= 0 {
				heap.Remove(&tw.timers, index)
			}

		case tw.sizeChan <- tw.timers.Len(): //写入数据的长度

		case <-tw.ctx.Done(): //如果ctx已经完成，结束代码
			tw.ticker.Stop()
			return

		case timer := <-tw.addChan: //添加时间轮
			heap.Push(&tw.timers, timer)

		case <-tw.ticker.C: //定时执行代码
			timers := tw.getExpired()  //获取超时的数据
			for _, t := range timers { //把数据写入到TimeOutChannel里面
				tw.TimeOutChannel() <- t.timeout
			}
			//更新数据
			tw.update(timers)
		}
	}
}
