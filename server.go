package tao

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/leesper/holmes"
)

func init() {
	netIdentifier = NewAtomicInt64(0)
}

var (
	//定义网络标识id
	netIdentifier *AtomicInt64
	tlsWrapper    func(net.Conn) net.Conn
)

//基本选项
type options struct {
	tlsCfg     *tls.Config//ssl选项
	codec      Codec//编码解码接口
	//定义四个方法
	onConnect  onConnectFunc
	onMessage  onMessageFunc
	onClose    onCloseFunc
	onError    onErrorFunc
	//工作的线程数据
	workerSize int  // numbers of worker go-routines
	//buffer的个数
	bufferSize int  // size of buffered channel
	//是否重连
	reconnect  bool // for ClientConn use only
}

//编辑option
// ServerOption sets server options.
type ServerOption func(*options)

//返回编辑connect的函数
// ReconnectOption returns a ServerOption that will make ClientConn reconnectable.
func ReconnectOption() ServerOption {
	return func(o *options) {
		o.reconnect = true
	}
}

//编码和解码
// CustomCodecOption returns a ServerOption that will apply a custom Codec.
func CustomCodecOption(codec Codec) ServerOption {
	return func(o *options) {
		o.codec = codec
	}
}

//ssl配置
// TLSCredsOption returns a ServerOption that will set TLS credentials for server
// connections.
func TLSCredsOption(config *tls.Config) ServerOption {
	return func(o *options) {
		o.tlsCfg = config
	}
}

// WorkerSizeOption returns a ServerOption that will set the number of go-routines
// in WorkerPool.
func WorkerSizeOption(workerSz int) ServerOption {
	return func(o *options) {
		o.workerSize = workerSz
	}
}

//buffer设置
// BufferSizeOption returns a ServerOption that is the size of buffered channel,
// for example an indicator of BufferSize256 means a size of 256.
func BufferSizeOption(indicator int) ServerOption {
	return func(o *options) {
		o.bufferSize = indicator
	}
}

// OnConnectOption returns a ServerOption that will set callback to call when new
// client connected.
func OnConnectOption(cb func(WriteCloser) bool) ServerOption {
	return func(o *options) {
		o.onConnect = cb
	}
}

// OnMessageOption returns a ServerOption that will set callback to call when new
// message arrived.
func OnMessageOption(cb func(Message, WriteCloser)) ServerOption {
	return func(o *options) {
		o.onMessage = cb
	}
}

// OnCloseOption returns a ServerOption that will set callback to call when client
// closed.
func OnCloseOption(cb func(WriteCloser)) ServerOption {
	return func(o *options) {
		o.onClose = cb
	}
}

// OnErrorOption returns a ServerOption that will set callback to call when error
// occurs.
func OnErrorOption(cb func(WriteCloser)) ServerOption {
	return func(o *options) {
		o.onError = cb
	}
}

//定义一个tcpServer
// Server  is a server to serve TCP requests.
type Server struct {
	opts   options//选项
	ctx    context.Context//上下文
	cancel context.CancelFunc//取消函数
	conns  *sync.Map//同步的map
	timing *TimingWheel//时间轮
	wg     *sync.WaitGroup//wg
	mu     sync.Mutex // guards following loc
	lis    map[net.Listener]bool//是否活着
	// for periodically running function every duration.
	interv time.Duration//时间间隔
	sched  onScheduleFunc//调度函数
}

// NewServer returns a new TCP server which has not started
// to serve requests yet.
func NewServer(opt ...ServerOption) *Server {
	var opts options
	for _, o := range opt {
		o(&opts)
	}

	//设置默认值
	if opts.codec == nil {
		opts.codec = TypeLengthValueCodec{}
	}
	if opts.workerSize <= 0 {
		opts.workerSize = defaultWorkersNum
	}
	if opts.bufferSize <= 0 {
		opts.bufferSize = BufferSize256
	}

	//初始化连接池
	// initiates go-routine pool instance
	globalWorkerPool = newWorkerPool(opts.workerSize)

	//初始化一个server
	s := &Server{
		opts:  opts,
		conns: &sync.Map{},
		wg:    &sync.WaitGroup{},
		lis:   make(map[net.Listener]bool),
	}
	//构建上下文
	s.ctx, s.cancel = context.WithCancel(context.Background())
	//构建时间轮
	s.timing = NewTimingWheel(s.ctx)
	return s
}

//返回个数
// ConnsSize returns connections size.
func (s *Server) ConnsSize() int {
	var sz int
	s.conns.Range(func(k, v interface{}) bool {
		sz++
		return true
	})
	return sz
}

//时间间隔
// Sched sets a callback to invoke every duration.
func (s *Server) Sched(dur time.Duration, sched func(time.Time, WriteCloser)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interv = dur
	//调度函数
	s.sched = onScheduleFunc(sched)
}

//server端的广播，广播消息
// Broadcast broadcasts message to all server connections managed.
func (s *Server) Broadcast(msg Message) {
	s.conns.Range(func(k, v interface{}) bool {
		c := v.(*ServerConn)
		if err := c.Write(msg); err != nil {
			holmes.Errorf("broadcast error %v, conn id %d", err, k.(int64))
			return false
		}
		return true
	})
}

//根据id，往con里面写Message
// Unicast unicasts message to a specified conn.
func (s *Server) Unicast(id int64, msg Message) error {
	v, ok := s.conns.Load(id)
	if ok {
		return v.(*ServerConn).Write(msg)
	}
	return fmt.Errorf("conn id %d not found", id)
}

//获取当前的server
// Conn returns a server connection with specified ID.
func (s *Server) Conn(id int64) (*ServerConn, bool) {
	v, ok := s.conns.Load(id)
	if ok {
		return v.(*ServerConn), ok
	}
	return nil, ok
}

//启动一个监听的接口
// Start starts the TCP server, accepting new clients and creating service
// go-routine for each. The service go-routines read messages and then call
// the registered handlers to handle them. Start returns when failed with fatal
// errors, the listener willl be closed when returned.
func (s *Server) Start(l net.Listener) error {
	s.mu.Lock()
	if s.lis == nil {
		s.mu.Unlock()
		l.Close()
		return ErrServerClosed
	}
	//记录监听的接口
	s.lis[l] = true
	s.mu.Unlock()

	//最后也是关闭这个连接
	defer func() {
		s.mu.Lock()
		if s.lis != nil && s.lis[l] {
			l.Close()
			delete(s.lis, l)
		}
		s.mu.Unlock()
	}()

	//接收到请求
	holmes.Infof("server start, net %s addr %s\n", l.Addr().Network(), l.Addr().String())

	//添加wg
	s.wg.Add(1)

	//超时的处理
	go s.timeOutLoop()

	//延时处理
	var tempDelay time.Duration
	for {
		//获取连接
		rawConn, err := l.Accept()
		if err != nil {
			//网路错误，设置尝试时间
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
				}

				//设置最长时间
				if max := 1 * time.Second; tempDelay >= max {
					tempDelay = max
				}
				holmes.Errorf("accept error %v, retrying in %d\n", err, tempDelay)

				select {
				case <-time.After(tempDelay)://设置时间
				case <-s.ctx.Done()://关闭ctx
				}
				continue
			}
			return err
		}
		//延时时间
		tempDelay = 0

		// how many connections do we have ?
		//连接的个数
		sz := s.ConnsSize()

		//大于最大连接，拒绝连接
		if sz >= MaxConnections {
			holmes.Warnf("max connections size %d, refuse\n", sz)
			rawConn.Close()
			continue
		}

		//设置tls连接
		if s.opts.tlsCfg != nil {
			rawConn = tls.Server(rawConn, s.opts.tlsCfg)
		}

		//获取网路的id
		netid := netIdentifier.GetAndIncrement()
		//新建一个连接
		sc := NewServerConn(netid, s, rawConn)
		//设置名字
		sc.SetName(sc.rawConn.RemoteAddr().String())

		//设置调度，间隔时间
		s.mu.Lock()
		if s.sched != nil {
			sc.RunEvery(s.interv, s.sched)
		}
		s.mu.Unlock()

		//存储网络id和sc
		s.conns.Store(netid, sc)
		addTotalConn(1)

		//添加数据
		s.wg.Add(1) // this will be Done() in ServerConn.Close()
		go func() {
			//开始处理连接
			sc.Start()
		}()

		//循环处理
		holmes.Infof("accepted client %s, id %d, total %d\n", sc.Name(), netid, s.ConnsSize())
		s.conns.Range(func(k, v interface{}) bool {
			i := k.(int64)
			c := v.(*ServerConn)
			holmes.Infof("client(%d) %s", i, c.Name())
			return true
		})
	} // for loop
}

//关闭server
// Stop gracefully closes the server, it blocked until all connections
// are closed and all go-routines are exited.
func (s *Server) Stop() {
	// immediately stop accepting new clients
	s.mu.Lock()
	listeners := s.lis
	s.lis = nil
	s.mu.Unlock()
	//关闭监听的端口
	for l := range listeners {
		l.Close()
		holmes.Infof("stop accepting at address %s\n", l.Addr().String())
	}

	//关闭所有的conns
	// close all connections
	conns := map[int64]*ServerConn{}

	//设置成基本的
	s.conns.Range(func(k, v interface{}) bool {
		i := k.(int64)
		c := v.(*ServerConn)
		conns[i] = c
		return true
	})

	//设置基本的
	// let GC do the cleanings
	s.conns = nil

	//关闭原始连接
	for _, c := range conns {
		c.rawConn.Close()
		holmes.Infof("close client %s\n", c.Name())
	}

	//取消context
	s.mu.Lock()
	s.cancel()
	s.mu.Unlock()

	//关闭连接
	s.wg.Wait()

	holmes.Infoln("server stopped gracefully, bye.")
	os.Exit(0)
}

// Retrieve the extra data(i.e. net id), and then redispatch timeout callbacks
// to corresponding client connection, this prevents one client from running
// callbacks of other clients
func (s *Server) timeOutLoop() {
	//最后关闭
	defer s.wg.Done()

	for {
		select {
		//关闭，直接返回
		case <-s.ctx.Done():
			return
		//获取OnTimeOut的chan
		case timeout := <-s.timing.TimeOutChannel():
			//获取id
			netID := timeout.Ctx.Value(netIDCtx).(int64)
			if v, ok := s.conns.Load(netID); ok {
				//获取ServerConn的连接
				sc := v.(*ServerConn)
				sc.timerCh <- timeout //超时的处理
			} else {
				//warn日志
				holmes.Warnf("invalid client %d", netID)
			}
		}
	}
}

//获取TLS的配置
// LoadTLSConfig returns a TLS configuration with the specified cert and key file.
func LoadTLSConfig(certFile, keyFile string, isSkipVerify bool) (*tls.Config, error) {
	//加载cert数据
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	//获取基本的配置
	config := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: isSkipVerify,
		CipherSuites: []uint16{
			tls.TLS_RSA_WITH_RC4_128_SHA,
			tls.TLS_RSA_WITH_3DES_EDE_CBC_SHA,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_RC4_128_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_RC4_128_SHA,
			tls.TLS_ECDHE_RSA_WITH_3DES_EDE_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		},
	}
	now := time.Now()
	config.Time = func() time.Time { return now }
	config.Rand = rand.Reader
	return config, nil
}
