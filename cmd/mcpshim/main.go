package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mcpshim/mcpshim/internal/client"
)

func main() {
	binary := filepath.Base(os.Args[0])
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println("mcpshim dev")
		os.Exit(0)
	}
	os.Exit(client.Run(binary, os.Args[1:]))
}
