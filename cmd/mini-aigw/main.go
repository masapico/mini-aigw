package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mini-aigw/internal/config"
	"mini-aigw/internal/db"
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
		case "backup":
			runBackupCLI(os.Args[2:])
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

func runBackupCLI(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dbFlag := fs.String("db", "", "Source SQLite database file path (default: data/gateway.db or from .env DATABASE_URL)")
	outFlag := fs.String("out", "", "Output path for the backup file (default: backups/gateway_YYYYMMDD_HHmmss.db)")

	_ = fs.Parse(args)

	cfg := config.LoadConfig()
	dbPath := *dbFlag
	if dbPath == "" {
		if cfg != nil && cfg.DatabaseURL != "" {
			dbPath = cfg.DatabaseURL
		} else {
			dbPath = "data/gateway.db"
		}
	}

	outPath := *outFlag
	if outPath == "" {
		timestamp := time.Now().Format("20060102_150405")
		outPath = filepath.Join("backups", fmt.Sprintf("gateway_%s.db", timestamp))
	}

	fmt.Printf("[mini-aigw backup] Starting hot backup from '%s' to '%s'...\n", dbPath, outPath)
	if err := db.CreateBackup(dbPath, outPath); err != nil {
		fmt.Fprintf(os.Stderr, "[mini-aigw backup] Error: %v\n", err)
		os.Exit(1)
	}

	fi, err := os.Stat(outPath)
	if err != nil {
		fmt.Printf("[mini-aigw backup] Backup created successfully: %s\n", outPath)
	} else {
		fmt.Printf("[mini-aigw backup] Backup created successfully: %s (%.2f KB)\n", outPath, float64(fi.Size())/1024.0)
	}
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
	fmt.Println("  backup    Perform a consistent hot backup of the SQLite database")
	fmt.Println("  eval      Run RAG evaluation & multi-model comparison CLI")
	fmt.Println("  version   Print version and build information")
	fmt.Println("  help      Show this help message")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  go build ./cmd/mini-aigw                                 # サーバーとツールを包含した単一バイナリをコンパイル")
	fmt.Println("  ./mini-aigw                                              # サーバー起動 (ポート8000)")
	fmt.Println("  ./mini-aigw backup -db data/gateway.db -out C:\\backup.db  # データベースのホットバックアップ")
	fmt.Println("  ./mini-aigw eval -dataset testdata/eval/sample_dataset.jsonl -dry-run")
	fmt.Println("==================================================")
}
