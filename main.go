package main

import (
	"fmt"
	"os"

	"github.com/guillermo/mwt/internal/cli"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cli.SetBuildInfo(cli.BuildInfo{
		Version: version,
		Commit:  commit,
		Date:    date,
	})
	code, err := cli.Run(os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}
