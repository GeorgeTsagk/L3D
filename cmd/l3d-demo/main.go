//go:build layertree

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/GeorgeTsagk/L3D/mvp"
)

func main() {
	btcd := flag.String("btcd", "", "path to the patched btcd binary")
	dir := flag.String("out", "", "empty artifact directory")
	liteClient := flag.String("lite-client", "", "run the daemon-free client demo with this binary")
	allModes := flag.Bool("all-modes", false, "demonstrate every supported tier and profile on one node")
	flag.Parse()
	if *btcd == "" || *dir == "" {
		flag.Usage()
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *allModes {
		if *liteClient == "" {
			fmt.Fprintln(os.Stderr, "all-modes requires lite-client")
			os.Exit(2)
		}
		if _, err := mvp.RunModes(ctx, mvp.LiteConfig{BTCD: *btcd, Client: *liteClient, Dir: *dir, Output: os.Stdout}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *liteClient != "" {
		if _, err := mvp.RunLite(ctx, mvp.LiteConfig{BTCD: *btcd, Client: *liteClient, Dir: *dir, Output: os.Stdout}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if _, err := mvp.Run(ctx, mvp.Config{BTCD: *btcd, Dir: *dir, Output: os.Stdout}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
