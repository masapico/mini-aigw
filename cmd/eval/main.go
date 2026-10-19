package main

import (
	"os"

	"mini-aigw/internal/eval"
)

func main() {
	eval.RunCLI(os.Args[1:])
}
