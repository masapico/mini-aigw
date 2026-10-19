package main

import (
	"os"

	"mini-aigw/internal/server"
)

func main() {
	server.Run(os.Args[1:])
}
