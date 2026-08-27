// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	// Embed the IANA timezone database so workspace timezone validation is
	// hermetic. Release builds use -trimpath, which prevents the runtime from
	// locating $GOROOT/lib/time/zoneinfo.zip, and minimal hosts (Windows,
	// scratch containers) ship no system tzdata.
	_ "time/tzdata"

	"github.com/fenandosr/mksrv/internal/cli"
	"github.com/fenandosr/mksrv/internal/tf"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

const modulePath = "github.com/fenandosr/mksrv"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := cli.New(cli.BuildInfo{
		Version: version, Commit: commit, Date: date,
		ModulePath: modulePath, TerraformVersion: tf.Version,
	}, os.Stdout, os.Stderr)
	if err := app.Execute(ctx, os.Args[1:]); err != nil {
		if !cli.AlreadyPrinted(err) {
			fmt.Fprintf(os.Stderr, "mksrv: %v\n", err)
		}
		os.Exit(cli.ExitCode(err))
	}
}
