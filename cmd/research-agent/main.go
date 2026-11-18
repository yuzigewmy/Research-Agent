package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"research-agent/internal/workbench"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "loopback IP address and port")
	dataDir := flag.String("data-dir", "data", "directory for the workspace database")
	webDir := flag.String("web-dir", "web/dist", "directory containing the built web app")
	flag.Parse()

	if err := run(*listen, *dataDir, *webDir); err != nil {
		log.Fatal(err)
	}
}

func run(address, dataDir, webDir string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("listen address must be a loopback IP and port, got %q", address)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()

	store, err := workbench.Open(dataDir)
	if err != nil {
		return err
	}
	defer store.Close()

	server := &http.Server{
		Handler:           workbench.NewHandler(store, os.DirFS(webDir)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("Listening at http://%s", listener.Addr())
	return server.Serve(listener)
}
