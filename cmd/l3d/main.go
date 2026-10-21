//go:build layertree

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/GeorgeTsagk/L3D/lite"
	"github.com/GeorgeTsagk/L3D/protocol"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

var (
	version = "development"
	commit  = "unknown"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	const usage = "usage: l3d modes|version|init|status|locate|attach|sync|join|update|exit|submit [flags]\nPrivate regtest testing only. Use COMMAND -h for flags."
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	command := args[0]
	if command == "help" || command == "-h" || command == "--help" {
		fmt.Fprintln(os.Stdout, usage)
		return nil
	}
	if command == "version" {
		fmt.Fprintf(os.Stdout, "l3d %s (%s), regtest only\n", version, commit)
		return nil
	}
	if command == "modes" {
		return json.NewEncoder(os.Stdout).Encode(protocol.Modes())
	}
	switch command {
	case "init", "status", "locate", "attach", "sync", "join", "update", "exit", "submit":
	default:
		return fmt.Errorf("unknown command %q\n%s", command, usage)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	configPath := fs.String("config", "", "client JSON configuration file")
	statePath := fs.String("state", "", "wallet state file")
	rpcPath := fs.String("rpc-config", "", "local btcd config file")
	proofPath := fs.String("proof", "", "membership or empty-slot proof file")
	request := fs.String("request", "", "signed proposal to submit")
	out := fs.String("out", "", "proposal file to write")
	funding := fs.String("funding", "", "fee and deposit UTXO as txid:vout")
	amount := fs.Uint64("amount", 0, "new leaf balance in sats")
	eviction := fs.Uint("evict-after", 0, "lease end block height")
	tier := fs.String("tier", "", "required tree tier when attaching, or sparse location tier")
	profile := fs.String("profile", "", "required tree profile when attaching, or sparse location profile")
	nonce := fs.Uint64("start-nonce", 0, "first nonce to try when locating a sparse slot")
	depth := fs.Int("depth", 31, "tree depth for sparse location")
	fee := fs.Int64("fee", 20000, "transaction fee in sats")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if err := applyConfig(fs, *configPath); err != nil {
		return err
	}
	if *statePath == "" {
		return fmt.Errorf("state is required")
	}
	if err := os.MkdirAll(filepath.Dir(*statePath), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(*statePath+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("wallet is in use")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if command == "init" {
		if _, err = os.Lstat(*statePath); !os.IsNotExist(err) {
			return fmt.Errorf("state file already exists or is inaccessible")
		}
		s, err := lite.New()
		if err != nil {
			return err
		}
		if err = s.Save(*statePath); err != nil {
			return err
		}
		status, err := s.Status()
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(status)
	}
	s, err := lite.Load(*statePath)
	if err != nil {
		return err
	}
	if command == "locate" {
		if *nonce > 0xffffffff {
			return fmt.Errorf("nonce overflows")
		}
		pos, nonce, err := s.LocateFrom(*tier, *profile, *depth, uint32(*nonce))
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			Position uint64 `json:"position"`
			Nonce    uint32 `json:"nonce"`
		}{pos, nonce})
	}
	if command == "status" {
		status, err := s.Status()
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(status)
	}
	rpc, err := lite.Dial(*rpcPath)
	if err != nil {
		return err
	}
	defer rpc.Shutdown()
	switch command {
	case "attach":
		var p lite.Proof
		if err = lite.ReadJSON(*proofPath, &p); err != nil {
			return err
		}
		config, err := p.Parameters()
		if err != nil {
			return err
		}
		if (*tier != "" && *tier != config.Tier) || (*profile != "" && *profile != config.Profile) {
			return fmt.Errorf("offered tree does not match selected mode")
		}
		if err = s.Attach(rpc, p); err != nil {
			return err
		}
	case "sync":
		if _, err = s.Sync(rpc); err != nil {
			return err
		}
	case "join", "update", "exit":
		if *out == "" || filepath.Clean(*out) == filepath.Clean(*statePath) {
			return fmt.Errorf("a separate proposal output path is required")
		}
		op, err := parseOutpoint(*funding)
		if err != nil {
			return err
		}
		if *eviction > 0xffffffff {
			return fmt.Errorf("eviction height overflows")
		}
		name := map[string]string{"join": "JOIN_0", "update": "UPDATE", "exit": "EXIT_1"}[command]
		spend, err := s.Prepare(rpc, name, *amount, uint32(*eviction), op, *fee)
		if err != nil {
			return err
		}
		if err = lite.WriteSpend(*out, spend); err != nil {
			return err
		}
	case "submit":
		spend, err := lite.ReadSpend(*request)
		if err != nil {
			return err
		}
		if _, err = s.Submit(rpc, spend); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	if err = s.Save(*statePath); err != nil {
		return err
	}
	status, err := s.Status()
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(status)
}
func parseOutpoint(s string) (wire.OutPoint, error) {
	var op wire.OutPoint
	p := strings.Split(s, ":")
	if len(p) != 2 {
		return op, fmt.Errorf("funding must be txid:vout")
	}
	h, err := chainhash.NewHashFromStr(p[0])
	if err != nil {
		return op, err
	}
	n, err := strconv.ParseUint(p[1], 10, 32)
	if err != nil {
		return op, err
	}
	op.Hash = *h
	op.Index = uint32(n)
	return op, nil
}
