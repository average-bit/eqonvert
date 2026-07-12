package main

import (
	"github.com/average-bit/eqonvert/cmd"
)

// version is injected at build time via -ldflags "-X main.version=...".
// Defaults to "dev" for plain `go build`.
var version = "dev"

func main() {
	cmd.Execute(version)
}
