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
	handleExported *expvar.Int
	connExported   *expvar.Int
	timeExported   *expvar.Float
	qpsExported    *expvar.Float
)

//初始化变量
func init() {
	handleExported = expvar.NewInt("TotalHandle")
	connExported = expvar.NewInt("TotalConn")
	timeExported = expvar.NewFloat("TotalTime")
	qpsExported = expvar.NewFloat("QPS")
}

//监听端口
// MonitorOn starts up an HTTP monitor on port.
func MonitorOn(port int) {
	go func() {
		if err := http.ListenAndServe(fmt.Sprintf(":%d", port), nil); err != nil {
			holmes.Errorln(err)
			return
		}
	}()
}

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
	totalConn, err := strconv.ParseInt(connExported.String(), 10, 64)
	if err != nil {
		holmes.Errorln(err)
		return
	}

	totalTime, err := strconv.ParseFloat(timeExported.String(), 64)
	if err != nil {
		holmes.Errorln(err)
		return
	}

	totalHandle, err := strconv.ParseInt(handleExported.String(), 10, 64)
	if err != nil {
		holmes.Errorln(err)
		return
	}

	if float64(totalConn)*totalTime != 0 {
		// take the average time per worker go-routine
		qps := float64(totalHandle) / (float64(totalConn) * (totalTime / float64(WorkerPoolInstance().Size())))
		qpsExported.Set(qps)
	}
}
