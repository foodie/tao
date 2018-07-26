package tao

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"reflect"
	"runtime"
	"time"
	"unsafe"
)

//未定义的错误
// ErrUndefined for undefined message type.
type ErrUndefined int32

//未定义的错误字符串
func (e ErrUndefined) Error() string {
	return fmt.Sprintf("undefined message type %d", e)
}

//自定义错误
// Error codes returned by failures dealing with server or connection.
var (
	ErrParameter     = errors.New("parameter error")
	ErrNilKey        = errors.New("nil key")
	ErrNilValue      = errors.New("nil value")
	ErrWouldBlock    = errors.New("would block")
	ErrNotHashable   = errors.New("not hashable")
	ErrNilData       = errors.New("nil data")
	ErrBadData       = errors.New("more than 8M data")
	ErrNotRegistered = errors.New("handler not registered")
	ErrServerClosed  = errors.New("server has been closed")
)

//常量
// definitions about some constants.
const (
	MaxConnections    = 1000
	BufferSize128     = 128
	BufferSize256     = 256
	BufferSize512     = 512
	BufferSize1024    = 1024
	defaultWorkersNum = 20
)

//连接函数的配置
type onConnectFunc func(WriteCloser) bool
type onMessageFunc func(Message, WriteCloser)
type onCloseFunc func(WriteCloser)
type onErrorFunc func(WriteCloser)

//工作函数
type workerFunc func()

//调度函数
type onScheduleFunc func(time.Time, WriteCloser)

//超时时的结构
// OnTimeOut represents a timed task.
type OnTimeOut struct {
	Callback func(time.Time, WriteCloser)
	Ctx      context.Context
}

//新建一个OnTimeOut
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
	switch kd {
	case reflect.Ptr, reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

//打印栈
func printStack() {
	var buf [4096]byte
	n := runtime.Stack(buf[:], false)
	os.Stderr.Write(buf[:n])
}
