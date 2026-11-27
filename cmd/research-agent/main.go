package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"research-agent/internal/inference"
	"research-agent/internal/workbench"
	webassets "research-agent/web"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "loopback IP address and port")
	dataDir := flag.String("data-dir", "data", "directory for the workspace database")
	webDir := flag.String("web-dir", "", "override the bundled or default Web build directory")
	restore := flag.String("restore", "", "将工作区 ZIP 恢复到 --data-dir 指定的新目录")
	flag.Parse()

	restoreRequested := false
	flag.Visit(func(option *flag.Flag) { restoreRequested = restoreRequested || option.Name == "restore" })
	if restoreRequested {
		if *restore == "" {
			log.Fatal("请为 --restore 指定工作区备份文件。")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		err := workbench.RestoreWorkspace(ctx, *restore, *dataDir)
		stop()
		if err != nil {
			log.Fatalf("恢复工作区失败：%v", err)
		}
		log.Printf("工作区已恢复至 %s。", *dataDir)
		return
	}
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

	config := inference.Config{
		BaseURL:         os.Getenv("RESEARCH_MODEL_BASE_URL"),
		Model:           os.Getenv("RESEARCH_MODEL_NAME"),
		APIKey:          os.Getenv("RESEARCH_MODEL_API_KEY"),
		ReasoningEffort: os.Getenv("RESEARCH_MODEL_REASONING_EFFORT"),
	}
	var model *inference.Client
	if config.BaseURL != "" || config.Model != "" {
		model, err = inference.New(config)
		if err != nil {
			return err
		}
	}
	var computeNode *workbench.ComputeNode
	if nodeURL, nodeToken := os.Getenv("RESEARCH_COMPUTE_NODE_URL"), os.Getenv("RESEARCH_COMPUTE_NODE_TOKEN"); nodeURL != "" || nodeToken != "" {
		computeNode, err = workbench.NewComputeNode(nodeURL, nodeToken)
		if err != nil {
			return err
		}
	}

	webFS, err := webassets.Files(webDir)
	if err != nil {
		return fmt.Errorf("open Web assets: %w", err)
	}
	handler := workbench.NewHandler(store, webFS, workbench.HandlerConfig{
		ComputeNode:                 computeNode,
		Model:                       model,
		ConversationModelName:       os.Getenv("RESEARCH_CONVERSATION_MODEL_NAME"),
		ConversationReasoningEffort: os.Getenv("RESEARCH_CONVERSATION_MODEL_REASONING_EFFORT"),
		WorkflowModelName:           os.Getenv("RESEARCH_WORKFLOW_MODEL_NAME"),
		WorkflowReasoningEffort:     os.Getenv("RESEARCH_WORKFLOW_MODEL_REASONING_EFFORT"),
		WorkflowModelTemplates:      os.Getenv("RESEARCH_WORKFLOW_MODEL_TEMPLATES"),
		ReviewModelName:             os.Getenv("RESEARCH_REVIEW_MODEL_NAME"),
		ReviewReasoningEffort:       os.Getenv("RESEARCH_REVIEW_MODEL_REASONING_EFFORT"),
		EditModelName:               os.Getenv("RESEARCH_EDIT_MODEL_NAME"),
		EditReasoningEffort:         os.Getenv("RESEARCH_EDIT_MODEL_REASONING_EFFORT"),
		VisionModelName:             os.Getenv("RESEARCH_VISION_MODEL_NAME"),
		VisionReasoningEffort:       os.Getenv("RESEARCH_VISION_MODEL_REASONING_EFFORT"),
		ImageModelName:              os.Getenv("RESEARCH_IMAGE_MODEL_NAME"),
		ImageModelQuality:           os.Getenv("RESEARCH_IMAGE_MODEL_QUALITY"),
		WordPython:                  os.Getenv("RESEARCH_WORD_PYTHON"),
		ScriptPython:                os.Getenv("RESEARCH_SCRIPT_PYTHON"),
		SoftwarePDFFont:             os.Getenv("RESEARCH_PDF_FONT"),
		LatexBin:                    os.Getenv("RESEARCH_LATEX_BIN"),
	})
	if err := store.ResumeQueuedTasks(context.Background(), model); err != nil {
		return err
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("Listening at http://%s", listener.Addr())
	return server.Serve(listener)
}
