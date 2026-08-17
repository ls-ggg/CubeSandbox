// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package cow

import "context"

// Sync state values for snapshot remote readiness (control-plane remote_ready).
const (
	SyncStatePending = "pending" // never synced
	SyncStateRunning = "running" // sync in progress
	SyncStateReady   = "ready"   // sync completed (remote_ready)
	SyncStateFailed  = "failed"
)

// SyncStatus is the mock/real sync progress for one snapshot id.
type SyncStatus struct {
	SnapshotID string `json:"snapshot_id"`
	State      string `json:"state"`
	Message    string `json:"message,omitempty"`
}

// Syncer is the optional S3-backend capability for uploading a local snapshot
// to remote storage and querying that upload. XFS Stores do not implement it.
type Syncer interface {
	// Sync triggers a (mock or real) upload of snapshotID. Mock succeeds
	// immediately and marks the snapshot Ready.
	Sync(ctx context.Context, snapshotID string) error
	// SyncStatus returns the last known sync state. Unknown ids report Pending.
	SyncStatus(ctx context.Context, snapshotID string) (*SyncStatus, error)
}

// Activator is the optional capability for opening a snapshot that already
// exists on this node. Real cross-node ingest will call into CubeCow; the
// mock only activates local objects and fails when they are missing.
type Activator interface {
	Activate(ctx context.Context, snapshotID string) error
}
