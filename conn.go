package tao

import (
	"context"
	"crypto/tls"
	"net"
	"sync"
	"time"

	"github.com/leesper/holmes"
)

//消息类型，消息长度，消息最大长度
const (
	// MessageTypeBytes is the length of type header.
	MessageTypeBytes = 4
	// MessageLenBytes is the length of length header.
	MessageLenBytes = 4
	// MessageMaxBytes is the maximum bytes allowed for application data.
	MessageMaxBytes = 1 << 23 // 8M
)


//消息处理器类型
// MessageHandler is a combination of message and its handler function.
type MessageHandler struct {
	message Message//消息接口
	handler HandlerFunc//消息处理函数
}

//写和关闭接口
// WriteCloser is the interface that groups Write and Close methods.
type WriteCloser interface {
	Write(Message) error
	Close()
}

//连接的结构
// ServerConn represents a server connection to a TCP server, it implments Conn.
type ServerConn struct {
	netid   int64//系统id
	belong  *Server//属于哪个server
	rawConn net.Conn//tcp连接

	once      *sync.Once//一次
	wg        *sync.WaitGroup//等待的group
	sendCh    chan []byte//发送的byte
	handlerCh chan MessageHandler//消息处理器
	timerCh   chan *OnTimeOut//时间的chan

	mu      sync.Mutex // guards following 加锁

	name    string//名字
	heart   int64//心跳
	pending []int64//添加
	ctx     context.Context//上下文
	cancel  context.CancelFunc//取消函数
}

// NewServerConn returns a new server connection which has not started to
// serve requests yet.
func NewServerConn(id int64, s *Server, c net.Conn) *ServerConn {
	//基本的连接
	sc := &ServerConn{
		netid:     id,
		belong:    s,
		rawConn:   c,
		once:      &sync.Once{},
		wg:        &sync.WaitGroup{},
		sendCh:    make(chan []byte, s.opts.bufferSize),
		handlerCh: make(chan MessageHandler, s.opts.bufferSize),
		timerCh:   make(chan *OnTimeOut, s.opts.bufferSize),
		heart:     time.Now().UnixNano(),
	}
	sc.ctx, sc.cancel = context.WithCancel(context.WithValue(s.ctx, serverCtx, s))
	sc.name = c.RemoteAddr().String()
	sc.pending = []int64{}
	return sc
}

//从ctx中获取Server
// ServerFromContext returns the server within the context.
func ServerFromContext(ctx context.Context) (*Server, bool) {
	server, ok := ctx.Value(serverCtx).(*Server)
	return server, ok
}

//获取连接的id
// NetID returns net ID of server connection.
func (sc *ServerConn) NetID() int64 {
	return sc.netid
}

//设置名字
// SetName sets name of server connection.
func (sc *ServerConn) SetName(name string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.name = name
}

//获取名字
// Name returns the name of server connection.
func (sc *ServerConn) Name() string {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	name := sc.name
	return name
}

//设置心跳heart
// SetHeartBeat sets the heart beats of server connection.
func (sc *ServerConn) SetHeartBeat(heart int64) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.heart = heart
}

//获取心跳
// HeartBeat returns the heart beats of server connection.
func (sc *ServerConn) HeartBeat() int64 {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	heart := sc.heart
	return heart
}

//设置ctx值
// SetContextValue sets extra data to server connection.
func (sc *ServerConn) SetContextValue(k, v interface{}) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.ctx = context.WithValue(sc.ctx, k, v)
}

//获取ctx数据
// ContextValue gets extra data from server connection.
func (sc *ServerConn) ContextValue(k interface{}) interface{} {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.ctx.Value(k)
}

//开始处理连接
// Start starts the server connection, creating go-routines for reading,
// writing and handlng.
func (sc *ServerConn) Start() {
	holmes.Infof("conn start, <%v -> %v>\n", sc.rawConn.LocalAddr(), sc.rawConn.RemoteAddr())

	//连接时的处理方法
	onConnect := sc.belong.opts.onConnect
	if onConnect != nil {
		onConnect(sc)
	}

	//定义三个loop函数
	//loop：读，写，处理
	loopers := []func(WriteCloser, *sync.WaitGroup){readLoop, writeLoop, handleLoop}
	for _, l := range loopers {
		looper := l
		sc.wg.Add(1)
		go looper(sc, sc.wg)
	}
}

//关闭
// Close gracefully closes the server connection. It blocked until all sub
// go-routines are completed and returned.
func (sc *ServerConn) Close() {
	//只关闭一次
	sc.once.Do(func() {
		holmes.Infof("conn close gracefully, <%v -> %v>\n", sc.rawConn.LocalAddr(), sc.rawConn.RemoteAddr())

		//关闭连接的处理方法
		// callback on close
		onClose := sc.belong.opts.onClose
		if onClose != nil {
			onClose(sc)
		}

		//删除指定的server的netid
		// remove connection from server
		sc.belong.conns.Delete(sc.netid)
		//减少统计连接数
		addTotalConn(-1)

		//丢弃未发送的连接，立即返回error
		// close net.Conn, any blocked read or write operation will be unblocked and
		// return errors.
		if tc, ok := sc.rawConn.(*net.TCPConn); ok {
			// avoid time-wait state
			//SetLinger设定当连接中仍有数据等待发送或接受时的Close方法的行为。
			/*
			如果sec < 0（默认），Close方法立即返回，操作系统停止后台数据发送；
			如果 sec == 0，Close立刻返回，操作系统丢弃任何未发送或未接收的数据；
			如果sec > 0，Close方法阻塞最多sec秒，等待数据发送或者接收，在一些操作系统中，在超时后，任何未发送的数据会被丢弃。
			 */
			tc.SetLinger(0)
		}
		//关闭连接
		sc.rawConn.Close()

		// cancel readLoop, writeLoop and handleLoop go-routines.
		sc.mu.Lock()
		sc.cancel()//取消当前的context
		//基本的处理
		pending := sc.pending
		sc.pending = nil
		sc.mu.Unlock()


		//取消时间的处理
		//取消待处理的pending
		// clean up pending timers
		for _, id := range pending {
			sc.CancelTimer(id)
		}

		//等待完成
		// wait until all go-routines exited.
		sc.wg.Wait()

		//关闭各种chan
		// close all channels and block until all go-routines exited.
		close(sc.sendCh)
		close(sc.handlerCh)
		close(sc.timerCh)

		//关闭wg
		// tell server I'm done :( .
		sc.belong.wg.Done()// context完成
	})
}

//添加时间的timer
// AddPendingTimer adds a timer ID to server Connection.
func (sc *ServerConn) AddPendingTimer(timerID int64) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.pending != nil {
		sc.pending = append(sc.pending, timerID)
	}
}

//异步写
// Write writes a message to the client.
func (sc *ServerConn) Write(message Message) error {
	return asyncWrite(sc, message)
}

//添加定时器id
// RunAt runs a callback at the specified timestamp.
func (sc *ServerConn) RunAt(timestamp time.Time, callback func(time.Time, WriteCloser)) int64 {
	id := runAt(sc.ctx, sc.netid, sc.belong.timing, timestamp, callback)
	if id >= 0 {
		//添加时间
		sc.AddPendingTimer(id)
	}
	return id
}

// RunAfter runs a callback right after the specified duration ellapsed.
func (sc *ServerConn) RunAfter(duration time.Duration, callback func(time.Time, WriteCloser)) int64 {
	id := runAfter(sc.ctx, sc.netid, sc.belong.timing, duration, callback)
	if id >= 0 {
		sc.AddPendingTimer(id)
	}
	return id
}

// RunEvery runs a callback on every interval time.
func (sc *ServerConn) RunEvery(interval time.Duration, callback func(time.Time, WriteCloser)) int64 {
	id := runEvery(sc.ctx, sc.netid, sc.belong.timing, interval, callback)
	if id >= 0 {
		sc.AddPendingTimer(id)
	}
	return id
}

//取消定时器
// CancelTimer cancels a timer with the specified ID.
func (sc *ServerConn) CancelTimer(timerID int64) {
	cancelTimer(sc.belong.timing, timerID)
}

//取消定时器
func cancelTimer(timing *TimingWheel, timerID int64) {
	if timing != nil {
		timing.CancelTimer(timerID)
	}
}

//远程地址
// RemoteAddr returns the remote address of server connection.
func (sc *ServerConn) RemoteAddr() net.Addr {
	return sc.rawConn.RemoteAddr()
}

//本地地址
// LocalAddr returns the local address of server connection.
func (sc *ServerConn) LocalAddr() net.Addr {
	return sc.rawConn.LocalAddr()
}

//客户端的结构
// ClientConn represents a client connection to a TCP server.
type ClientConn struct {
	addr      string//地址
	opts      options//选项
	netid     int64//网路id
	rawConn   net.Conn//连接
	once      *sync.Once// 一次
	wg        *sync.WaitGroup//等待
	sendCh    chan []byte//发送chan的
	handlerCh chan MessageHandler//消息处理器
	timing    *TimingWheel//时间轮
	mu        sync.Mutex // guards following
	name      string//名字
	heart     int64//心跳
	pending   []int64//添加
	ctx       context.Context//ctx
	cancel    context.CancelFunc//cancel
}

// NewClientConn returns a new client connection which has not started to
// serve requests yet.
func NewClientConn(netid int64, c net.Conn, opt ...ServerOption) *ClientConn {
	var opts options
	for _, o := range opt {
		o(&opts)
	}
	//默认的解析器
	if opts.codec == nil {
		opts.codec = TypeLengthValueCodec{}
	}
	//buffersize
	if opts.bufferSize <= 0 {
		opts.bufferSize = BufferSize256
	}
	//新建一个客户端连接
	return newClientConnWithOptions(netid, c, opts)
}

//新建一个连接对象
func newClientConnWithOptions(netid int64, c net.Conn, opts options) *ClientConn {
	cc := &ClientConn{
		addr:      c.RemoteAddr().String(),
		opts:      opts,
		netid:     netid,
		rawConn:   c,
		once:      &sync.Once{},
		wg:        &sync.WaitGroup{},
		sendCh:    make(chan []byte, opts.bufferSize),
		handlerCh: make(chan MessageHandler, opts.bufferSize),
		heart:     time.Now().UnixNano(),
	}
	cc.ctx, cc.cancel = context.WithCancel(context.Background())
	//时间轮
	cc.timing = NewTimingWheel(cc.ctx)
	cc.name = c.RemoteAddr().String()
	cc.pending = []int64{}
	return cc
}

// NetID returns the net ID of client connection.
func (cc *ClientConn) NetID() int64 {
	return cc.netid
}

// SetName sets the name of client connection.
func (cc *ClientConn) SetName(name string) {
	cc.mu.Lock()
	cc.name = name
	cc.mu.Unlock()
}

// Name gets the name of client connection.
func (cc *ClientConn) Name() string {
	cc.mu.Lock()
	name := cc.name
	cc.mu.Unlock()
	return name
}

// SetHeartBeat sets the heart beats of client connection.
func (cc *ClientConn) SetHeartBeat(heart int64) {
	cc.mu.Lock()
	cc.heart = heart
	cc.mu.Unlock()
}

// HeartBeat gets the heart beats of client connection.
func (cc *ClientConn) HeartBeat() int64 {
	cc.mu.Lock()
	heart := cc.heart
	cc.mu.Unlock()
	return heart
}

// SetContextValue sets extra data to client connection.
func (cc *ClientConn) SetContextValue(k, v interface{}) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.ctx = context.WithValue(cc.ctx, k, v)
}

// ContextValue gets extra data from client connection.
func (cc *ClientConn) ContextValue(k interface{}) interface{} {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return cc.ctx.Value(k)
}

//开始
// Start starts the client connection, creating go-routines for reading,
// writing and handlng.
func (cc *ClientConn) Start() {
	holmes.Infof("conn start, <%v -> %v>\n", cc.rawConn.LocalAddr(), cc.rawConn.RemoteAddr())
	onConnect := cc.opts.onConnect
	if onConnect != nil {
		onConnect(cc)
	}
	//处理连接
	loopers := []func(WriteCloser, *sync.WaitGroup){readLoop, writeLoop, handleLoop}
	for _, l := range loopers {
		looper := l
		cc.wg.Add(1)
		go looper(cc, cc.wg)
	}
}

// Close gracefully closes the client connection. It blocked until all sub
// go-routines are completed and returned.
func (cc *ClientConn) Close() {
	cc.once.Do(func() {
		holmes.Infof("conn close gracefully, <%v -> %v>\n", cc.rawConn.LocalAddr(), cc.rawConn.RemoteAddr())

		// callback on close
		onClose := cc.opts.onClose
		if onClose != nil {
			onClose(cc)
		}

		// close net.Conn, any blocked read or write operation will be unblocked and
		// return errors.
		cc.rawConn.Close()

		// cancel readLoop, writeLoop and handleLoop go-routines.
		cc.mu.Lock()
		cc.cancel()
		cc.pending = nil
		cc.mu.Unlock()

		// stop timer
		cc.timing.Stop()

		// wait until all go-routines exited.
		cc.wg.Wait()

		// close all channels.
		close(cc.sendCh)
		close(cc.handlerCh)

		// cc.once is a *sync.Once. After reconnect() returned, cc.once will point
		// to a newly-allocated one while other go-routines such as readLoop,
		// writeLoop and handleLoop blocking on the old *sync.Once continue to
		// execute Close() (and of course do nothing because of sync.Once).
		// NOTE that it will cause an "unlock of unlocked mutex" error if cc.once is
		// a sync.Once struct, because "defer o.m.Unlock()" in sync.Once.Do() will
		// be performed on an unlocked mutex(the newly-allocated one noticed above)
		if cc.opts.reconnect {//重写连接
			cc.reconnect()
		}
	})
}


// reconnect reconnects and returns a new *ClientConn.
func (cc *ClientConn) reconnect() {
	var c net.Conn
	var err error
	//加密的连接
	if cc.opts.tlsCfg != nil {
		c, err = tls.Dial("tcp", cc.addr, cc.opts.tlsCfg)
		if err != nil {
			holmes.Fatalln("tls dial error", err)
		}
	} else {
		c, err = net.Dial("tcp", cc.addr)
		if err != nil {
			holmes.Fatalln("net dial error", err)
		}
	}
	// copy the newly-created *ClientConn to cc, so after
	// reconnect returned cc will be updated to new one.
	//新建一个连接
	*cc = *newClientConnWithOptions(cc.netid, c, cc.opts)
	cc.Start()
}

// Write writes a message to the client.
func (cc *ClientConn) Write(message Message) error {
	return asyncWrite(cc, message)
}

// RunAt runs a callback at the specified timestamp.
func (cc *ClientConn) RunAt(timestamp time.Time, callback func(time.Time, WriteCloser)) int64 {
	id := runAt(cc.ctx, cc.netid, cc.timing, timestamp, callback)
	if id >= 0 {
		cc.AddPendingTimer(id)
	}
	return id
}

// RunAfter runs a callback right after the specified duration ellapsed.
func (cc *ClientConn) RunAfter(duration time.Duration, callback func(time.Time, WriteCloser)) int64 {
	id := runAfter(cc.ctx, cc.netid, cc.timing, duration, callback)
	if id >= 0 {
		cc.AddPendingTimer(id)
	}
	return id
}

// RunEvery runs a callback on every interval time.
func (cc *ClientConn) RunEvery(interval time.Duration, callback func(time.Time, WriteCloser)) int64 {
	id := runEvery(cc.ctx, cc.netid, cc.timing, interval, callback)
	if id >= 0 {
		cc.AddPendingTimer(id)
	}
	return id
}

// AddPendingTimer adds a new timer ID to client connection.
func (cc *ClientConn) AddPendingTimer(timerID int64) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	//增加时间id
	if cc.pending != nil {
		cc.pending = append(cc.pending, timerID)
	}
}

// CancelTimer cancels a timer with the specified ID.
func (cc *ClientConn) CancelTimer(timerID int64) {
	cancelTimer(cc.timing, timerID)
}

//获取远程ip
// RemoteAddr returns the remote address of server connection.
func (cc *ClientConn) RemoteAddr() net.Addr {
	return cc.rawConn.RemoteAddr()
}

//后去本地ip
// LocalAddr returns the local address of server connection.
func (cc *ClientConn) LocalAddr() net.Addr {
	return cc.rawConn.LocalAddr()
}

//新增一个网络id是时间
func runAt(ctx context.Context, netID int64, timing *TimingWheel, ts time.Time, cb func(time.Time, WriteCloser)) int64 {
	timeout := NewOnTimeOut(NewContextWithNetID(ctx, netID), cb)
	return timing.AddTimer(ts, 0, timeout)
}
//新增一个定时器，
func runAfter(ctx context.Context, netID int64, timing *TimingWheel, d time.Duration, cb func(time.Time, WriteCloser)) int64 {
	delay := time.Now().Add(d)
	return runAt(ctx, netID, timing, delay, cb)
}
//新增一个时间
func runEvery(ctx context.Context, netID int64, timing *TimingWheel, d time.Duration, cb func(time.Time, WriteCloser)) int64 {
	delay := time.Now().Add(d)
	timeout := NewOnTimeOut(NewContextWithNetID(ctx, netID), cb)
	return timing.AddTimer(delay, d, timeout)
}

//异步写
func asyncWrite(c interface{}, m Message) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = ErrServerClosed
		}
	}()

	var (
		pkt    []byte
		sendCh chan []byte
	)
	//连接的类型
	switch c := c.(type) {
	//设置服务端的con
	case *ServerConn:
		pkt, err = c.belong.opts.codec.Encode(m)
		sendCh = c.sendCh

	case *ClientConn://设置客户端的con
		pkt, err = c.opts.codec.Encode(m)
		sendCh = c.sendCh
	}

	//处理错误
	if err != nil {
		holmes.Errorf("asyncWrite error %v\n", err)
		return
	}

	//发送数据
	select {
	case sendCh <- pkt:
		err = nil
	default:
		err = ErrWouldBlock
	}
	return
}

//读的循环
/* readLoop() blocking read from connection, deserialize bytes into message,
then find corresponding handler, put it into channel */
func readLoop(c WriteCloser, wg *sync.WaitGroup) {
	var (
		rawConn          net.Conn
		codec            Codec
		cDone            <-chan struct{}
		sDone            <-chan struct{}
		setHeartBeatFunc func(int64)
		onMessage        onMessageFunc
		handlerCh        chan MessageHandler
		msg              Message
		err              error
	)

	//获取基本的类型
	switch c := c.(type) {
	case *ServerConn:
		rawConn = c.rawConn
		codec = c.belong.opts.codec
		cDone = c.ctx.Done()
		sDone = c.belong.ctx.Done()
		setHeartBeatFunc = c.SetHeartBeat
		onMessage = c.belong.opts.onMessage
		handlerCh = c.handlerCh
	case *ClientConn:
		rawConn = c.rawConn
		codec = c.opts.codec
		cDone = c.ctx.Done()
		sDone = nil
		setHeartBeatFunc = c.SetHeartBeat
		onMessage = c.opts.onMessage
		handlerCh = c.handlerCh
	}

	//结束后
	defer func() {
		if p := recover(); p != nil {
			holmes.Errorf("panics: %v\n", p)
		}
		wg.Done()
		holmes.Debugln("readLoop go-routine exited")
		c.Close()
	}()

	for {
		select {
		case <-cDone: // connection closed c完成
			holmes.Debugln("receiving cancel signal from conn")
			return
		case <-sDone: // server closed server完成
			holmes.Debugln("receiving cancel signal from server")
			return
		default:
			//解码
			msg, err = codec.Decode(rawConn)
			if err != nil {
				holmes.Errorf("error decoding message %v\n", err)
				if _, ok := err.(ErrUndefined); ok {
					// update heart beats
					//心跳函数
					setHeartBeatFunc(time.Now().UnixNano())
					continue
				}
				return
			}
			//心跳
			setHeartBeatFunc(time.Now().UnixNano())
			//获取处理器
			handler := GetHandlerFunc(msg.MessageNumber())
			if handler == nil {
				if onMessage != nil {
					holmes.Infof("message %d call onMessage()\n", msg.MessageNumber())
					//消息的处理
					onMessage(msg, c.(WriteCloser))
				} else {
					holmes.Warnf("no handler or onMessage() found for message %d\n", msg.MessageNumber())
				}
				continue
			}
			//发送数据到handlerCh
			handlerCh <- MessageHandler{msg, handler}
		}
	}
}


//写数据
/* writeLoop() receive message from channel, serialize it into bytes,
then blocking write into connection */
func writeLoop(c WriteCloser, wg *sync.WaitGroup) {
	var (
		rawConn net.Conn
		sendCh  chan []byte
		cDone   <-chan struct{}
		sDone   <-chan struct{}
		pkt     []byte
		err     error
	)

	//获取连接数据
	switch c := c.(type) {
	case *ServerConn:
		rawConn = c.rawConn
		sendCh = c.sendCh
		cDone = c.ctx.Done()
		sDone = c.belong.ctx.Done()
	case *ClientConn:
		rawConn = c.rawConn
		sendCh = c.sendCh
		cDone = c.ctx.Done()
		sDone = nil
	}

	//结束时的处理
	defer func() {
		if p := recover(); p != nil {
			holmes.Errorf("panics: %v\n", p)
		}
		// drain all pending messages before exit
	OuterFor:
		for {
			select {
			//获取数据
			//写入数据
			case pkt = <-sendCh:
				if pkt != nil {
					if _, err = rawConn.Write(pkt); err != nil {
						holmes.Errorf("error writing data %v\n", err)
					}
				}
			default:
				break OuterFor
			}
		}
		//关闭连接
		wg.Done()
		holmes.Debugln("writeLoop go-routine exited")
		c.Close()
	}()

	for {
		select {
		//各种错误的处理
		case <-cDone: // connection closed
			holmes.Debugln("receiving cancel signal from conn")
			return
		case <-sDone: // server closed
			holmes.Debugln("receiving cancel signal from server")
			return
			//接收数据，写入数据
		case pkt = <-sendCh:
			if pkt != nil {
				if _, err = rawConn.Write(pkt); err != nil {
					holmes.Errorf("error writing data %v\n", err)
					return
				}
			}
		}
	}
}

// handleLoop() - put handler or timeout callback into worker go-routines
func handleLoop(c WriteCloser, wg *sync.WaitGroup) {
	//处理连接
	var (
		cDone        <-chan struct{}
		sDone        <-chan struct{}
		timerCh      chan *OnTimeOut
		handlerCh    chan MessageHandler
		netID        int64
		ctx          context.Context
		askForWorker bool
		err          error
	)

	//连接处理
	switch c := c.(type) {
	case *ServerConn:
		cDone = c.ctx.Done()
		sDone = c.belong.ctx.Done()
		timerCh = c.timerCh
		handlerCh = c.handlerCh
		netID = c.netid
		ctx = c.ctx
		askForWorker = true
	case *ClientConn:
		cDone = c.ctx.Done()
		sDone = nil
		timerCh = c.timing.timeOutChan
		handlerCh = c.handlerCh
		netID = c.netid
		ctx = c.ctx
	}

	//关闭连接
	defer func() {
		if p := recover(); p != nil {
			holmes.Errorf("panics: %v\n", p)
		}
		wg.Done()
		holmes.Debugln("handleLoop go-routine exited")
		c.Close()
	}()

	for {
		select {
		case <-cDone: // connectin closed
			holmes.Debugln("receiving cancel signal from conn")
			return
		case <-sDone: // server closed
			holmes.Debugln("receiving cancel signal from server")
			return
		case msgHandler := <-handlerCh://消息的处理
			msg, handler := msgHandler.message, msgHandler.handler
			if handler != nil {
				//如果是异步处理消息
				if askForWorker {
					err = WorkerPoolInstance().Put(netID, func() {
						handler(NewContextWithNetID(NewContextWithMessage(ctx, msg), netID), c)
					})
					if err != nil {
						holmes.Errorln(err)
					}
					addTotalHandle()
				} else {
					//同步处理消息
					handler(NewContextWithNetID(NewContextWithMessage(ctx, msg), netID), c)
				}
			}
		case timeout := <-timerCh://超时处理
			if timeout != nil {
				timeoutNetID := NetIDFromContext(timeout.Ctx)
				if timeoutNetID != netID {
					holmes.Errorf("timeout net %d, conn net %d, mismatched!\n", timeoutNetID, netID)
				}
				//同步直接调用，异步则分开处理
				if askForWorker {
					err = WorkerPoolInstance().Put(netID, func() {
						timeout.Callback(time.Now(), c.(WriteCloser))
					})
					if err != nil {
						holmes.Errorln(err)
					}
				} else {
					timeout.Callback(time.Now(), c.(WriteCloser))
				}
			}
		}
	}
}
