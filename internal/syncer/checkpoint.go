package syncer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/go-mysql-org/go-mysql/mysql"
)

// Checkpoint persists binlog position for resume.
type Checkpoint struct {
	Name string `json:"name"`
	Pos  uint32 `json:"pos"`
	GTID string `json:"gtid,omitempty"`
}

type PositionStore struct {
	path string
	mu   sync.Mutex
}

func NewPositionStore(path string) *PositionStore {
	return &PositionStore{path: path}
}

func (p *PositionStore) Load() (*Checkpoint, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	data, err := os.ReadFile(p.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("parse checkpoint: %w", err)
	}
	return &cp, nil
}

func (p *PositionStore) Save(pos mysql.Position, gtid string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if gtid == "" {
		if data, err := os.ReadFile(p.path); err == nil {
			var prev Checkpoint
			if json.Unmarshal(data, &prev) == nil && prev.GTID != "" {
				gtid = prev.GTID
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		return err
	}
	cp := Checkpoint{Name: pos.Name, Pos: pos.Pos, GTID: gtid}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p.path)
}

func (p *PositionStore) ToMySQLPosition(cp *Checkpoint) mysql.Position {
	if cp == nil {
		return mysql.Position{}
	}
	return mysql.Position{Name: cp.Name, Pos: cp.Pos}
}
