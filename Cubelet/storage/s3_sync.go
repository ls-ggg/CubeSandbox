// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/tencentcloud/CubeSandbox/Cubelet/storage/cow"
)

// mock sync state on S3Cow. Real remote upload will replace this map later;
// create/activate/delete paths stay on the copied XFS-like Store methods.
type s3SyncEntry struct {
	state   string
	message string
}

// Sync implements [cow.Syncer]. Mock: mark snapshot Ready immediately (no
// network). Idempotent — re-syncing an already-ready id stays Ready.
func (m *S3Cow) Sync(ctx context.Context, snapshotID string) error {
	_ = ctx
	id := strings.TrimSpace(snapshotID)
	if id == "" {
		return fmt.Errorf("snapshot_id is required")
	}
	m.syncLock.Lock()
	defer m.syncLock.Unlock()
	if m.syncStates == nil {
		m.syncStates = make(map[string]s3SyncEntry)
	}
	m.syncStates[id] = s3SyncEntry{state: cow.SyncStateReady, message: "mock sync ok"}
	return nil
}

// SyncStatus implements [cow.Syncer].
func (m *S3Cow) SyncStatus(ctx context.Context, snapshotID string) (*cow.SyncStatus, error) {
	_ = ctx
	id := strings.TrimSpace(snapshotID)
	if id == "" {
		return nil, fmt.Errorf("snapshot_id is required")
	}
	m.syncLock.Lock()
	defer m.syncLock.Unlock()
	if m.syncStates == nil {
		return &cow.SyncStatus{SnapshotID: id, State: cow.SyncStatePending}, nil
	}
	entry, ok := m.syncStates[id]
	if !ok {
		return &cow.SyncStatus{SnapshotID: id, State: cow.SyncStatePending}, nil
	}
	return &cow.SyncStatus{SnapshotID: id, State: entry.state, Message: entry.message}, nil
}

var _ cow.Syncer = (*S3Cow)(nil)
