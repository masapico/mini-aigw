package main

import (
	"fmt"
	"os"

	"mini-aigw/internal/eval"
	"mini-aigw/internal/server"
	"mini-aigw/internal/version"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "eval":
			eval.RunCLI(os.Args[2:])
			return
		case "server":
			server.Run(os.Args[2:])
			return
		case "version", "--version", "-v":
			fmt.Printf("mini-aigw %s (commit: %s, built: %s)\n", version.Version, version.GitCommit, version.BuildTime)
			return
		case "help", "--help", "-h":
			printHelp()
			return
		}
	}

	// コマンド未指定時はデフォルトでサーバーを起動
	server.Run(os.Args[1:])
}

func printHelp() {
	fmt.Println("==================================================")
	fmt.Printf("mini-aigw %s - 閉域AWS環境向けAIゲートウェイ\n", version.Version)
	fmt.Println("==================================================")
	fmt.Println("Usage:")
	fmt.Println("  mini-aigw [command] [flags]")
	fmt.Println()
	fmt.Println("Available Commands:")
	fmt.Println("  server    Start the HTTP gateway and management console (default)")
	fmt.Println("  eval      Run RAG evaluation & multi-model comparison CLI")
	fmt.Println("  version   Print version and build information")
	fmt.Println("  help      Show this help message")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  go build ./cmd/mini-aigw                                 # サーバーと評価ツールを包含した単一バイナリをコンパイル")
	fmt.Println("  ./mini-aigw                                              # サーバー起動 (ポート8000)")
	fmt.Println("  ./mini-aigw eval -dataset testdata/eval/sample_dataset.jsonl -dry-run")
	fmt.Println("==================================================")
}

