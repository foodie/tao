package tao

import (
	"expvar"
	"fmt"
	"net/http"
	"strconv"

	"github.com/leesper/holmes"
	"io"
)	

//定义原子操作
var (
	handleExported *expvar.Int//处理数量
	connExported   *expvar.Int//连接数量
	timeExported   *expvar.Float//时间数量
	qpsExported    *expvar.Float//qps数量
)

//初始化变量
func init() {
	handleExported = expvar.NewInt("TotalHandle")
	connExported = expvar.NewInt("TotalConn")
	timeExported = expvar.NewFloat("TotalTime")
	qpsExported = expvar.NewFloat("QPS")
}

//对外暴露一个http接口
// MonitorOn starts up an HTTP monitor on port.
func MonitorOn(port int) {
	go func() {
		if err := http.ListenAndServe(fmt.Sprintf(":%d", port), nil); err != nil {
			holmes.Errorln(err)
			return
		}
	}()
}

//定义qps
func ShowQps(port int)  {
	http.HandleFunc("/show_qps", showIt)

	go func() {
		err := http.ListenAndServe(":11111", nil)
		holmes.Errorln(err)
	}()
}
//显示qps
func showIt(w http.ResponseWriter, req *http.Request) {
	io.WriteString(w, qpsExported.String())
}




/**
增加连接，处理次数，处理时间
	1 连接，处理，时间

**/
func addTotalConn(delta int64) {
	connExported.Add(delta)
	calculateQPS()
}

func addTotalHandle() {
	handleExported.Add(1)
	calculateQPS()
}

func addTotalTime(seconds float64) {
	timeExported.Add(seconds)
	calculateQPS()
}

//计算qps
func calculateQPS() {
	//总连接
	totalConn, err := strconv.ParseInt(connExported.String(), 10, 64)
	if err != nil {
		holmes.Errorln(err)
		return
	}
	//总时间
	totalTime, err := strconv.ParseFloat(timeExported.String(), 64)
	if err != nil {
		holmes.Errorln(err)
		return
	}
	//从处理次数
	totalHandle, err := strconv.ParseInt(handleExported.String(), 10, 64)
	if err != nil {
		holmes.Errorln(err)
		return
	}

	//如果连接和时间相乘不为0
	if float64(totalConn)*totalTime != 0 {
		// take the average time per worker go-routine
		//处理数量/连接/时间/池大小
		qps := float64(totalHandle) / (float64(totalConn) * (totalTime / float64(WorkerPoolInstance().Size())))
		qpsExported.Set(qps)
	}
}
