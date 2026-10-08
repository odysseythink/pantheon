package main

import (
	"os"

	"github.com/odysseythink/pantheon/memory"
)

func main() {
	os.Exit(memory.MainCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
