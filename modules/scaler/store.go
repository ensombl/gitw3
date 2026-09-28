// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package scaler

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3" // SQLite driver for the scaler state
)

// NodeState is a worker's lifecycle state.
type NodeState string

const (
	NodeProvisioning NodeState = "provisioning"
	NodeJoining      NodeState = "joining"
	NodeReady        NodeState = "ready"
	NodeFailed       NodeState = "failed"
	NodeDraining     NodeState = "draining"
	NodeDeleting     NodeState = "deleting"
	NodeRemoving     NodeState = "removing"
	NodeDestroyed    NodeState = "destroyed"
)

// Node is a worker droplet the scaler tracks.
type Node struct {
	DropletID   string
	Name        string
	SwarmNodeID string
	State       NodeState
	PrivateIP   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Error       string
}

// NodeStore persists nodes and policy state in SQLite.
type NodeStore struct {
	db *sql.DB
}

// OpenNodeStore opens (and migrates) the SQLite state file.
func OpenNodeStore(path string) (*NodeStore, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS nodes (
	droplet_id    TEXT PRIMARY KEY,
	name          TEXT NOT NULL,
	swarm_node_id TEXT NOT NULL DEFAULT '',
	state         TEXT NOT NULL,
	private_ip    TEXT NOT NULL DEFAULT '',
	created_at    INTEGER NOT NULL,
	updated_at    INTEGER NOT NULL,
	error         TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS kv (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate scaler state: %w", err)
	}
	return &NodeStore{db: db}, nil
}

func (s *NodeStore) Close() error {
	return s.db.Close()
}

// Save inserts or updates a node.
func (s *NodeStore) Save(node *Node) error {
	if node.UpdatedAt.IsZero() {
		node.UpdatedAt = time.Now().UTC()
	}
	if node.CreatedAt.IsZero() {
		node.CreatedAt = node.UpdatedAt
	}
	_, err := s.db.Exec(`
INSERT INTO nodes (droplet_id, name, swarm_node_id, state, private_ip, created_at, updated_at, error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(droplet_id) DO UPDATE SET
	name = excluded.name, swarm_node_id = excluded.swarm_node_id, state = excluded.state,
	private_ip = excluded.private_ip, updated_at = excluded.updated_at, error = excluded.error`,
		node.DropletID, node.Name, node.SwarmNodeID, string(node.State), node.PrivateIP,
		node.CreatedAt.Unix(), node.UpdatedAt.Unix(), node.Error)
	return err
}

// Delete forgets a destroyed node.
func (s *NodeStore) Delete(dropletID string) error {
	_, err := s.db.Exec(`DELETE FROM nodes WHERE droplet_id = ?`, dropletID)
	return err
}

// List returns every tracked node.
func (s *NodeStore) List() ([]Node, error) {
	rows, err := s.db.Query(`SELECT droplet_id, name, swarm_node_id, state, private_ip, created_at, updated_at, error FROM nodes ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := make([]Node, 0, 8)
	for rows.Next() {
		var node Node
		var state string
		var created, updated int64
		if err := rows.Scan(&node.DropletID, &node.Name, &node.SwarmNodeID, &state, &node.PrivateIP, &created, &updated, &node.Error); err != nil {
			return nil, err
		}
		node.State = NodeState(state)
		node.CreatedAt, node.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

func (s *NodeStore) getTime(key string) (time.Time, error) {
	var value int64
	err := s.db.QueryRow(`SELECT CAST(value AS INTEGER) FROM kv WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) || value == 0 {
		return time.Time{}, nil
	}
	return time.Unix(value, 0).UTC(), err
}

func (s *NodeStore) setTime(key string, value time.Time) error {
	unix := int64(0)
	if !value.IsZero() {
		unix = value.Unix()
	}
	_, err := s.db.Exec(`INSERT INTO kv (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, fmt.Sprint(unix))
	return err
}

// LoadPolicyState restores the policy's memory.
func (s *NodeStore) LoadPolicyState() (PolicyState, error) {
	var state PolicyState
	var err error
	if state.LastScaleUp, err = s.getTime("last_scale_up"); err != nil {
		return state, err
	}
	if state.LastScaleDown, err = s.getTime("last_scale_down"); err != nil {
		return state, err
	}
	state.LowSince, err = s.getTime("low_since")
	return state, err
}

// SavePolicyState persists the policy's memory.
func (s *NodeStore) SavePolicyState(state PolicyState) error {
	for key, value := range map[string]time.Time{
		"last_scale_up": state.LastScaleUp, "last_scale_down": state.LastScaleDown, "low_since": state.LowSince,
	} {
		if err := s.setTime(key, value); err != nil {
			return err
		}
	}
	return nil
}

// LastTokenRotation and SetLastTokenRotation track join token rotation.
func (s *NodeStore) LastTokenRotation() (time.Time, error) {
	return s.getTime("last_token_rotation")
}

func (s *NodeStore) SetLastTokenRotation(at time.Time) error {
	return s.setTime("last_token_rotation", at)
}
