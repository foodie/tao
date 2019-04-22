package tao

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"

	"github.com/leesper/holmes"
)

//心跳值
const (
	// HeartBeat is the default heart beat message number.
	HeartBeat = 0
)

//处理器接口
//包含，ctx和interface{}
// Handler takes the responsibility to handle incoming messages.
type Handler interface {
	Handle(context.Context, interface{})
}

//函数类型
//处理器函数，ctx和writeCloser
// HandlerFunc serves as an adapter to allow the use of ordinary functions as handlers.
type HandlerFunc func(context.Context, WriteCloser)
// Handle calls f(ctx, c)
func (f HandlerFunc) Handle(ctx context.Context, c WriteCloser) {
	f(ctx, c)
}

//反解得到消息内容的函数
//把byte解析成消息的函数
// UnmarshalFunc unmarshals bytes into Message.
type UnmarshalFunc func([]byte) (Message, error)


//1 处理实现了writer和close接口的连接
//2 反解[]byte 为Message
//处理上下文，反解消息类
// handlerUnmarshaler is a combination of unmarshal and handle functions for message.
type handlerUnmarshaler struct {
	handler     HandlerFunc//处理上下文，处理context
	unmarshaler UnmarshalFunc//反解byte成消息， 反解
}


var (
	buf *bytes.Buffer//定义buffer
	// messageRegistry is the registry of all
	// message-related unmarshal and handle functions.
	messageRegistry map[int32]handlerUnmarshaler//注册消息ctx处理器和字符反解器
)

func init() {
	//初始化数据
	messageRegistry = map[int32]handlerUnmarshaler{}
	buf = new(bytes.Buffer)
}


//注册消息处理器
// Register registers the unmarshal and handle functions for msgType.
// If no unmarshal function provided, the message will not be parsed.
// If no handler function provided, the message will not be handled unless you
// set a default one by calling SetOnMessageCallback.
// If Register being called twice on one msgType, it will panics.
func Register(msgType int32,
	unmarshaler func([]byte) (Message, error),
	handler func(context.Context, WriteCloser)) {
	if _, ok := messageRegistry[msgType]; ok {
		panic(fmt.Sprintf("trying to register message %d twice", msgType))
	}

	messageRegistry[msgType] = handlerUnmarshaler{
		unmarshaler: unmarshaler,
		handler:     HandlerFunc(handler),
	}
}

//获取消息反解器
// GetUnmarshalFunc returns the corresponding unmarshal function for msgType.
func GetUnmarshalFunc(msgType int32) UnmarshalFunc {
	entry, ok := messageRegistry[msgType]
	if !ok {
		return nil
	}
	return entry.unmarshaler
}

//获取处理器
// GetHandlerFunc returns the corresponding handler function for msgType.
func GetHandlerFunc(msgType int32) HandlerFunc {
	entry, ok := messageRegistry[msgType]
	if !ok {
		return nil
	}
	return entry.handler
}

//定义消息接口
//1 类型id  2 序列化
// Message represents the structured data that can be handled.
type Message interface {
	MessageNumber() int32//消息的类型
	Serialize() ([]byte, error)//序列化消息
}

//心跳消息
// HeartBeatMessage for application-level keeping alive.
type HeartBeatMessage struct {
	Timestamp int64
}

//把心跳消息序列化成字节数组
//重置消息，写入心跳消息
// Serialize serializes HeartBeatMessage into bytes.
func (hbm HeartBeatMessage) Serialize() ([]byte, error) {
	buf.Reset()//重置buf
	//写入大端消息
	err := binary.Write(buf, binary.LittleEndian, hbm.Timestamp)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

//心跳消息类型
// MessageNumber returns message number.
func (hbm HeartBeatMessage) MessageNumber() int32 {
	return HeartBeat
}


//把byte反解成message和error
//把byte数据转换成消息类
// DeserializeHeartBeat deserializes bytes into Message.
func DeserializeHeartBeat(data []byte) (message Message, err error) {
	var timestamp int64
	if data == nil {
		return nil, ErrNilData
	}
	buf := bytes.NewReader(data)
	err = binary.Read(buf, binary.LittleEndian, &timestamp)
	if err != nil {
		return nil, err
	}
	return HeartBeatMessage{
		Timestamp: timestamp,
	}, nil
}

//发送心跳
// HandleHeartBeat updates connection heart beat timestamp.
func HandleHeartBeat(ctx context.Context, c WriteCloser) {
	//从ctx获取,心跳内容
	msg := MessageFromContext(ctx)
	switch c := c.(type) {
	case *ServerConn://服务端接连接，发送心跳
		c.SetHeartBeat(msg.(HeartBeatMessage).Timestamp)
	case *ClientConn://客户端连接，发送心跳
		c.SetHeartBeat(msg.(HeartBeatMessage).Timestamp)
	}
}

//编码接口
// Codec is the interface for message coder and decoder.
// Application programmer can define a custom codec themselves.
type Codec interface {
	Decode(net.Conn) (Message, error)//解码
	Encode(Message) ([]byte, error)//编码
}

//定义一个长度和值的解码器
//类型，4位长度，值
// TypeLengthValueCodec defines a special codec.
// Format: type-length-value |4 bytes|4 bytes|n bytes <= 8M|
type TypeLengthValueCodec struct{}


//反解消息
// Decode decodes the bytes data into Message
func (codec TypeLengthValueCodec) Decode(raw net.Conn) (Message, error) {

	//存储消息类型的数据
	byteChan := make(chan []byte)
	//错误的chan
	errorChan := make(chan error)

	//读取消息内容
	go func(bc chan []byte, ec chan error) {
		//类型字节数组
		typeData := make([]byte, MessageTypeBytes)
		//从连接里面读取类型数据
		_, err := io.ReadFull(raw, typeData)
		if err != nil {
			//读取失败，直接返回数据
			ec <- err
			close(bc)
			close(ec)
			holmes.Debugln("go-routine read message type exited")
			return
		}
		bc <- typeData
	}(byteChan, errorChan)

	var typeBytes []byte

	select {
	case err := <-errorChan://读取消息类型失败
		return nil, err

	case typeBytes = <-byteChan://获取消息类型
		if typeBytes == nil {
			holmes.Warnln("read type bytes nil")
			return nil, ErrBadData
		}
		//构建一个类型Bytes的buf
		typeBuf := bytes.NewReader(typeBytes)
		var msgType int32
		if err := binary.Read(typeBuf, binary.LittleEndian, &msgType); err != nil {
			return nil, err
		}
		//获取消息的长度的bytes
		lengthBytes := make([]byte, MessageLenBytes)
		_, err := io.ReadFull(raw, lengthBytes)
		if err != nil {
			return nil, err
		}
		//获取消息的bytes转换成数据
		lengthBuf := bytes.NewReader(lengthBytes)
		var msgLen uint32
		if err = binary.Read(lengthBuf, binary.LittleEndian, &msgLen); err != nil {
			return nil, err
		}
		//消息太长
		if msgLen > MessageMaxBytes {
			holmes.Errorf("message(type %d) has bytes(%d) beyond max %d\n", msgType, msgLen, MessageMaxBytes)
			return nil, ErrBadData
		}
		//读取消息内容
		// read application data
		msgBytes := make([]byte, msgLen)
		_, err = io.ReadFull(raw, msgBytes)
		if err != nil {
			return nil, err
		}

		//获取注册到的消息反解器
		// deserialize message from bytes
		unmarshaler := GetUnmarshalFunc(msgType)
		if unmarshaler == nil {
			return nil, ErrUndefined(msgType)
		}
		//获取反解出来的消息
		return unmarshaler(msgBytes)
	}
}

//定义一个长度和内容的编码器
// Encode encodes the message into bytes data.
func (codec TypeLengthValueCodec) Encode(msg Message) ([]byte, error) {

	//获取二进制数据
	data, err := msg.Serialize()
	if err != nil {
		return nil, err
	}

	//新建一个buffer
	buf := new(bytes.Buffer)
	//写入消息类型，消息的长度，消息的内容
	binary.Write(buf, binary.LittleEndian, msg.MessageNumber())
	binary.Write(buf, binary.LittleEndian, int32(len(data)))
	buf.Write(data)
	packet := buf.Bytes()
	return packet, nil
}

//别名
// ContextKey is the key type for putting context-related data.
type contextKey string

// Context keys for messge, server and net ID.
//context的key
const (
	messageCtx contextKey = "message"//消息
	serverCtx  contextKey = "server"//服务端
	netIDCtx   contextKey = "netid"//网络
)

//在这个上下文放置消息的值
// NewContextWithMessage returns a new Context that carries message.
func NewContextWithMessage(ctx context.Context, msg Message) context.Context {
	return context.WithValue(ctx, messageCtx, msg)
}

//获取上下文中消息的值
// MessageFromContext extracts a message from a Context.
func MessageFromContext(ctx context.Context) Message {
	return ctx.Value(messageCtx).(Message)
}
//WithValue它是为了生成一个绑定了一个键值对数据的Context，这个绑定的数据可以通过Context.Value方法访问到
// NewContextWithNetID returns a new Context that carries net ID.
//放置网络id的值
func NewContextWithNetID(ctx context.Context, netID int64) context.Context {
	return context.WithValue(ctx, netIDCtx, netID)
}

//获取网路id的值
// NetIDFromContext returns a net ID from a Context.
func NetIDFromContext(ctx context.Context) int64 {
	return ctx.Value(netIDCtx).(int64)
}
