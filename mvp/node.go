//go:build layertree

package mvp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/btcsuite/btcd/rpcclient"
)

// startNode starts a private node with no P2P listener or outbound peers.
func startNode(ctx context.Context, binary, dir, miningAddress string) (*rpcclient.Client, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return nil, nil, err
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return nil, nil, err
	}
	pass := hex.EncodeToString(secret)
	log, err := os.Create(filepath.Join(dir, "btcd.log"))
	if err != nil {
		return nil, nil, err
	}
	conf := "regtest=1\ntxindex=1\nexperimental-layertree=1\nnotls=1\nnolisten=1\nnodnsseed=1\nmaxpeers=0\nrpcuser=demo\nrpcpass=" + pass + "\nrpclisten=" + addr + "\nminingaddr=" + miningAddress + "\n"
	confPath := filepath.Join(dir, "btcd.conf")
	if err = os.WriteFile(confPath, []byte(conf), 0600); err != nil {
		log.Close()
		return nil, nil, err
	}
	cmd := exec.CommandContext(ctx, binary, "--configfile="+confPath, "--datadir="+filepath.Join(dir, "chain"), "--logdir="+filepath.Join(dir, "logs"))
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		log.Close()
		return nil, nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	client, err := rpcclient.New(&rpcclient.ConnConfig{Host: addr, User: "demo", Pass: pass, HTTPPostMode: true, DisableTLS: true}, nil)
	cleanup := func() {
		if client != nil {
			client.Shutdown()
		}
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		_ = log.Close()
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			log.Close()
			client.Shutdown()
			return nil, nil, fmt.Errorf("btcd exited: %v, see %s", err, filepath.Join(dir, "btcd.log"))
		case <-ctx.Done():
			cleanup()
			return nil, nil, ctx.Err()
		case <-deadline.C:
			cleanup()
			return nil, nil, fmt.Errorf("btcd RPC startup timeout, see %s", filepath.Join(dir, "btcd.log"))
		case <-tick.C:
			if _, err := client.GetBlockCount(); err == nil {
				return client, cleanup, nil
			}
		}
	}
}
