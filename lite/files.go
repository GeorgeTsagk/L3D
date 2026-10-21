//go:build layertree

package lite

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/GeorgeTsagk/L3D/txn"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/wire"
)

// ReadJSON bounds untrusted proposal and proof files before decoding them.
func ReadJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4*1024*1024+1))
	if err != nil {
		return err
	}
	if len(b) > 4*1024*1024 {
		return fmt.Errorf("file too large")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err = dec.Decode(v); err != nil {
		return err
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}
func WriteJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".layertree-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func Load(path string) (*State, error) {
	var s State
	if err := ReadJSON(path, &s); err != nil {
		return nil, err
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}
func (s *State) Save(path string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	return WriteJSON(path, s)
}

type proposal struct {
	Tx           string
	Leaf         txn.TapLeaf
	ControlBlock []byte
	Stack        [][]byte
	Sigs         []txn.SigSlot
	Prevouts     []Coin
}

func WriteSpend(path string, s *txn.Spend) error {
	var raw bytes.Buffer
	if err := s.Tx.Serialize(&raw); err != nil {
		return err
	}
	p := proposal{Tx: hex.EncodeToString(raw.Bytes()), Leaf: s.Leaf, ControlBlock: s.ControlBlock, Stack: s.Stack, Sigs: s.Sigs}
	for _, in := range s.Tx.TxIn {
		prev := s.PrevOuts[in.PreviousOutPoint]
		if prev == nil {
			return fmt.Errorf("missing proposal prevout")
		}
		p.Prevouts = append(p.Prevouts, Coin{in.PreviousOutPoint, *prev})
	}
	return WriteJSON(path, p)
}
func ReadSpend(path string) (*txn.Spend, error) {
	var p proposal
	if err := ReadJSON(path, &p); err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(p.Tx)
	if err != nil {
		return nil, err
	}
	tx := wire.NewMsgTx(2)
	r := bytes.NewReader(raw)
	if err = tx.Deserialize(r); err != nil {
		return nil, err
	}
	if r.Len() != 0 {
		return nil, fmt.Errorf("trailing transaction bytes")
	}
	prevs := make(map[wire.OutPoint]*wire.TxOut)
	for i := range p.Prevouts {
		c := &p.Prevouts[i]
		if _, ok := prevs[c.OutPoint]; ok {
			return nil, fmt.Errorf("duplicate prevout")
		}
		prevs[c.OutPoint] = &c.Output
	}
	for _, in := range tx.TxIn {
		if prevs[in.PreviousOutPoint] == nil {
			return nil, fmt.Errorf("missing prevout")
		}
	}
	for _, slot := range p.Sigs {
		if slot.Index < 0 || slot.Index >= len(p.Stack) {
			return nil, fmt.Errorf("invalid signature slot")
		}
	}
	return &txn.Spend{Tx: tx, PrevOuts: prevs, Leaf: p.Leaf, ControlBlock: p.ControlBlock, Stack: p.Stack, Sigs: p.Sigs}, nil
}

// Dial reads the same local RPC configuration as the private regtest node.
// The client opens HTTP connections only during a command and closes them
// when the command finishes. This backend is trusted, not an SPV peer.
func Dial(path string) (*rpcclient.Client, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	values := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) == 2 {
			values[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	if err = scanner.Err(); err != nil {
		return nil, err
	}
	host := values["rpclisten"]
	if !strings.HasPrefix(host, "127.0.0.1:") || values["notls"] != "1" {
		return nil, fmt.Errorf("MVP RPC backend must use localhost and notls=1")
	}
	rpc, err := rpcclient.New(&rpcclient.ConnConfig{Host: host, User: values["rpcuser"], Pass: values["rpcpass"], HTTPPostMode: true, DisableTLS: true}, nil)
	if err != nil {
		return nil, err
	}
	if err = CheckNetwork(rpc); err != nil {
		rpc.Shutdown()
		return nil, err
	}
	return rpc, nil
}
