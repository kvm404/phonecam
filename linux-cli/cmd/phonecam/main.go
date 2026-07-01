package main

import (
	"context"
	"os"

	"github.com/kvm404/phonecam/linux-cli/internal/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), cli.OSSystem{}, os.Args[1:], os.Stdout, os.Stderr))
}
