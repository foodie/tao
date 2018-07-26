// Worker pool is a pool of go-routines running for executing callbacks,
// each client's message handler is permanently hashed into one specified
// worker to execute, so it is in-order for each client's perspective.

package tao

import (
	"time"
)

//工作池
// WorkerPool is a pool of go-routines running functions.
type WorkerPool struct {
	workers   []*worker
	closeChan chan struct{}
}

//全局的pool
var (
	globalWorkerPool *WorkerPool
)

//返回全局pool
// WorkerPoolInstance returns the global pool.
func WorkerPoolInstance() *WorkerPool {
	return globalWorkerPool
}

//创建一个工作池
func newWorkerPool(vol int) *WorkerPool {
	if vol <= 0 {
		vol = defaultWorkersNum
	}

	pool := &WorkerPool{
		workers:   make([]*worker, vol),
		closeChan: make(chan struct{}),
	}

	//初始化worker
	for i := range pool.workers {
		pool.workers[i] = newWorker(i, 1024, pool.closeChan)
		if pool.workers[i] == nil {
			panic("worker nil")
		}
	}

	return pool
}

//放置工作cb
// Put appends a function to some worker's channel.
func (wp *WorkerPool) Put(k interface{}, cb func()) error {
	code := hashCode(k)
	return wp.workers[code&uint32(len(wp.workers)-1)].put(workerFunc(cb))
}

//关闭工作池
// Close closes the pool, stopping it from executing functions.
func (wp *WorkerPool) Close() {
	close(wp.closeChan)
}

//返回尺寸
// Size returns the size of pool.
func (wp *WorkerPool) Size() int {
	return len(wp.workers)
}

type worker struct {
	index        int             //索引
	callbackChan chan workerFunc //工作的chan
	closeChan    chan struct{}   //是否关闭
}

func newWorker(i int, c int, closeChan chan struct{}) *worker {
	w := &worker{
		index:        i,                        //index
		callbackChan: make(chan workerFunc, c), //工作chan
		closeChan:    closeChan,                //关闭
	}
	go w.start()
	return w
}

//执行代码，计算qps
func (w *worker) start() {
	for {
		select {
		case <-w.closeChan:
			return
		case cb := <-w.callbackChan:
			before := time.Now()
			cb()
			addTotalTime(time.Since(before).Seconds())
		}
	}
}

//放入工作池
func (w *worker) put(cb workerFunc) error {
	select {
	case w.callbackChan <- cb:
		return nil
	default:
		return ErrWouldBlock
	}
}
