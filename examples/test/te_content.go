package main

import (
	"context"
	"log"
	"os"
	"time"
)

var logg *log.Logger

func someHandler() {
	//返回返回一个继承的Context,这个Context在父Context的Done被关闭时关闭自己的Done通道，或者在自己被Cancel的时候关闭自己的Done。
	//WithCancel同时还返回一个取消函数cancel，这个cancel用于取消当前的Context。
	ctx, cancel := context.WithCancel(context.Background())
	go doStuff(ctx)

	//10秒后取消doStuff
	time.Sleep(10 * time.Second)
	cancel()
	time.Sleep(1 * time.Second)

}

//每1秒work一下，同时会判断ctx是否被取消了，如果是就退出
func doStuff(ctx context.Context) {
	for {
		time.Sleep(1 * time.Second)
		select {
		case <-ctx.Done():
			logg.Printf("done")
			return
		default:
			logg.Printf("work")
		}
	}
}

func main() {
	//新建一个日志
	logg = log.New(os.Stdout, "", log.Ltime)
	someHandler()
	//输出数据
	logg.Printf("down")
}
