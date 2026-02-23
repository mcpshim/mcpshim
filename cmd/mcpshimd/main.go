package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/server"
)

func main() {
	configPath := flag.String("config", config.DefaultConfigPath(), "path to mcpshim config")
	socketPath := flag.String("socket", "", "override unix socket path")
	debug := flag.Bool("debug", false, "debug logging")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()

	if *showVersion {
		fmt.Println("mcpshimd dev")
		os.Exit(0)
	}

	cfg, err := config.LoadOrInit(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	if *socketPath != "" {
		cfg.Server.SocketPath = *socketPath
	}

	srv := server.New(*configPath, cfg)
	srv.SetDebug(*debug)
	if err := srv.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}
