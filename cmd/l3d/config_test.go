//go:build layertree

package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GeorgeTsagk/L3D/lite"
)

func configFlags(t *testing.T, args ...string) *flag.FlagSet {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("state", "", "")
	fs.String("rpc-config", "", "")
	fs.String("tier", "", "")
	fs.String("profile", "", "")
	fs.Int("depth", 31, "")
	fs.Int64("fee", 20000, "")
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return fs
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "l3d.json")
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigDefaultsAndOverrides(t *testing.T) {
	p := writeConfig(t, `{"network":"regtest","state":"wallet.json","rpc_config":"btcd.conf","tier":"ccv","profile":"dense","depth":8,"fee_sat":2000}`)
	fs := configFlags(t)
	if err := applyConfig(fs, p); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"state":      filepath.Join(filepath.Dir(p), "wallet.json"),
		"rpc-config": filepath.Join(filepath.Dir(p), "btcd.conf"),
		"tier":       "ccv", "profile": "dense", "depth": "8", "fee": "2000",
	} {
		if got := fs.Lookup(name).Value.String(); got != want {
			t.Fatalf("%s=%s, want %s", name, got, want)
		}
	}
	fs = configFlags(t, "-state", "relative-to-cwd.json", "-tier", "mid", "-fee", "0", "-rpc-config", "")
	if err := applyConfig(fs, p); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"state": "relative-to-cwd.json", "tier": "mid", "fee": "0", "rpc-config": ""} {
		if got := fs.Lookup(name).Value.String(); got != want {
			t.Fatalf("explicit %s=%s, want %s", name, got, want)
		}
	}
}

func TestConfigRejectsInvalidInput(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field":   `{"fees": 100}`,
		"trailing JSON":   `{} {}`,
		"bad JSON":        `{`,
		"wrong type":      `{"fee_sat":"100"}`,
		"mainnet":         `{"network":"mainnet"}`,
		"testnet":         `{"network":"testnet"}`,
		"invalid tier":    `{"tier":"unknown"}`,
		"invalid profile": `{"profile":"unknown"}`,
		"MIN sparse":      `{"tier":"min","profile":"sparse"}`,
		"short C0 sparse": `{"tier":"mid","profile":"sparse","depth":24}`,
		"deep C0 sparse":  `{"tier":"ccv0","profile":"sparse","depth":32}`,
		"zero depth":      `{"depth":0}`,
		"excess depth":    `{"depth":64}`,
		"negative fee":    `{"fee_sat":-1}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := applyConfig(configFlags(t), writeConfig(t, body)); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	if err := applyConfig(configFlags(t), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing config accepted")
	}
	if err := applyConfig(configFlags(t, "-fee", "-1"), ""); err == nil {
		t.Fatal("negative flag accepted without config")
	}
}

func TestSampleConfig(t *testing.T) {
	if err := applyConfig(configFlags(t), "../../sample-l3d.json"); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"full", "mid", "min", "ccv", "ccv0"} {
		for _, shape := range []string{"dense", "sparse"} {
			if mode == "min" && shape == "sparse" {
				continue
			}
			fs := configFlags(t, "-tier", mode, "-profile", shape)
			if err := applyConfig(fs, writeConfig(t, `{}`)); err != nil {
				t.Fatalf("%s %s: %v", mode, shape, err)
			}
		}
	}
}

func TestConfiguredWalletCommands(t *testing.T) {
	p := writeConfig(t, `{"state":"alice.json","tier":"ccv","profile":"dense","depth":8}`)
	if err := run([]string{"init", "-config", p}); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(filepath.Dir(p), "alice.json")
	if _, err := lite.Load(statePath); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"status", "-config", p}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"init", "-config", p}); err == nil {
		t.Fatal("existing wallet overwritten")
	}
	if err := run([]string{"status", "-config", p, "unexpected"}); err == nil {
		t.Fatal("positional argument accepted")
	}
	if err := run([]string{"unknown", "-config", p}); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("wrong error: %v", err)
	}
}
