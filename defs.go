package tao

import (
	"errors"
	"fmt"
	"time"
	"unsafe"
	"hash/fnv"
	"reflect"
	"runtime"
	"os"
	"context"
)


//未定义的错误的类型
// ErrUndefined for undefined message type.
type ErrUndefined int32

//未定义的错误字符串
func (e ErrUndefined) Error() string {
	return fmt.Sprintf("undefined message type %d", e)
}

//自定义错误
// Error codes returned by failures dealing with server or connection.
var (
	ErrParameter     = errors.New("parameter error")//参数
	ErrNilKey        = errors.New("nil key")//空key
	ErrNilValue      = errors.New("nil value")//空val
	ErrWouldBlock    = errors.New("would block")//阻塞
	ErrNotHashable   = errors.New("not hashable")//hashable错误
	ErrNilData       = errors.New("nil data")//空data
	ErrBadData       = errors.New("more than 8M data")//数据大于8M
	ErrNotRegistered = errors.New("handler not registered")//未注册处理器
	ErrServerClosed  = errors.New("server has been closed")//被关闭
)

//常量
// definitions about some constants.
const (
	MaxConnections    = 1000//最大连接

	//buffer的大小
	BufferSize128     = 128
	BufferSize256     = 256
	BufferSize512     = 512
	BufferSize1024    = 1024

	//默认的work的数量
	//默认的工作连接的个数
	defaultWorkersNum = 20
)

//连接函数的配置

/**

定义四种类型的函数：

	1 连接处理函数
	2 消息处理函数
	3 关闭处理函数
	4 错误处理函数

//写和关闭接口
type WriteCloser interface {
	Write(Message) error
	Close()
}

*/

//连接时处理
type onConnectFunc func(WriteCloser) bool
//处理消息
type onMessageFunc func(Message, WriteCloser)
//关闭时处理
type onCloseFunc func(WriteCloser)
//发生错误时的处理
type onErrorFunc func(WriteCloser)

//工作函数
type workerFunc func()

//调度函数，时间和WriteCloser
type onScheduleFunc func(time.Time, WriteCloser)

//超时时的结构
// OnTimeOut represents a timed task.
type OnTimeOut struct {
	Callback func(time.Time, WriteCloser)//回调函数
	Ctx      context.Context//上下文
}

//新建一个OnTimeOut，回调函数
// NewOnTimeOut returns OnTimeOut.
func NewOnTimeOut(ctx context.Context, cb func(time.Time, WriteCloser)) *OnTimeOut {
	return &OnTimeOut{
		Callback: cb,
		Ctx:      ctx,
	}
}

//hash接口
// Hashable is a interface for hashable object.
type Hashable interface {
	HashCode() int32
}

//int的尺寸
const intSize = unsafe.Sizeof(1)

//hash函数
//按照字符串构建hashcode
func hashCode(k interface{}) uint32 {
	var code uint32
	h := fnv.New32a()
	switch v := k.(type) {
	case bool:
		h.Write((*((*[1]byte)(unsafe.Pointer(&v))))[:])
		code = h.Sum32()
	case int:
		h.Write((*((*[intSize]byte)(unsafe.Pointer(&v))))[:])
		code = h.Sum32()
	case int8:
		h.Write((*((*[1]byte)(unsafe.Pointer(&v))))[:])
		code = h.Sum32()
	case int16:
		h.Write((*((*[2]byte)(unsafe.Pointer(&v))))[:])
		code = h.Sum32()
	case int32:
		h.Write((*((*[4]byte)(unsafe.Pointer(&v))))[:])
		code = h.Sum32()
	case int64:
		h.Write((*((*[8]byte)(unsafe.Pointer(&v))))[:])
		code = h.Sum32()
	case uint:
		h.Write((*((*[intSize]byte)(unsafe.Pointer(&v))))[:])
		code = h.Sum32()
	case uint8:
		h.Write((*((*[1]byte)(unsafe.Pointer(&v))))[:])
		code = h.Sum32()
	case uint16:
		h.Write((*((*[2]byte)(unsafe.Pointer(&v))))[:])
		code = h.Sum32()
	case uint32:
		h.Write((*((*[4]byte)(unsafe.Pointer(&v))))[:])
		code = h.Sum32()
	case uint64:
		h.Write((*((*[8]byte)(unsafe.Pointer(&v))))[:])
		code = h.Sum32()
	case string:
		h.Write([]byte(v))
		code = h.Sum32()
	case Hashable:
		c := v.HashCode()
		h.Write((*((*[4]byte)(unsafe.Pointer(&c))))[:])
		code = h.Sum32()
	default:
		panic("key not hashable")
	}
	return code
}

//判断是否是nil
func isNil(v interface{}) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	kd := rv.Type().Kind()

	//获取val，类型
	switch kd {//如果是基本类型，判断是否是nil
	case reflect.Ptr, reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

//打印栈,只显示调用栈
func printStack() {
	var buf [4096]byte
	n := runtime.Stack(buf[:], false)
	os.Stderr.Write(buf[:n])
}
