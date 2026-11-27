package main

import (
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"research-agent/internal/workbench"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8081", "计算节点监听的回环 IP 和端口")
	dataDir := flag.String("data-dir", "compute-data", "计算回执目录")
	name := flag.String("name", "配对计算节点", "工作台显示的节点名称")
	flag.Parse()

	host, _, err := net.SplitHostPort(*listen)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("计算节点须监听回环 IP 和端口。远程部署可在节点前配置 HTTPS 反向代理。")
	}
	worker, err := workbench.OpenComputeWorker(*dataDir, *name, os.Getenv("RESEARCH_COMPUTE_NODE_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}
	defer worker.Close()
	server := &http.Server{
		Addr:              *listen,
		Handler:           worker.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      25 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("计算节点监听 %s", listener.Addr())
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
