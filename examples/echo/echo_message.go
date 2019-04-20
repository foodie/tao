package echo

import (
	"context"

	"github.com/leesper/holmes"
	"github.com/leesper/tao"
)

//定义消息类型
// Message defines the echo message.
type Message struct {
	Content string
}

// Serialize serializes Message into bytes.
func (em Message) Serialize() ([]byte, error) {
	return []byte(em.Content), nil
}

// MessageNumber returns message type number.
func (em Message) MessageNumber() int32 {
	return 1
}

//反解消息
//解码
// DeserializeMessage deserializes bytes into Message.
func DeserializeMessage(data []byte) (message tao.Message, err error) {
	if data == nil {
		return nil, tao.ErrNilData
	}
	msg := string(data)
	echo := Message{
		Content: msg,
	}
	return echo, nil
}

//处理上下文
//处理消息
// ProcessMessage process the logic of echo message.
func ProcessMessage(ctx context.Context, conn tao.WriteCloser) {
	msg := tao.MessageFromContext(ctx).(Message)
	holmes.Infof("receving message %s\n", msg.Content)
	conn.Write(msg)
}
