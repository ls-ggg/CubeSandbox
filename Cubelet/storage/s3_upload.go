// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package storage

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/cubecow"
	"github.com/tencentcloud/CubeSandbox/Cubelet/storage/cow"
)

type s3UploadEntry struct {
	state   string
	message string
	uuids   *cow.RemoteUUIDs
}

type cowSnapshotExporter interface {
	ExportSnapshot(name string) (string, error)
}

type cowVolumeImporter interface {
	ImportLvol(name, remoteUUID string) (string, error)
}

// Upload implements [cow.Uploader]. Calls cubecow_export_snapshot per
// snapshot disk (rootfs / memory / metadata). Current S3 mock reuses the
// XFS cubecow engine, which returns precondition-failed; then a stable
// mock uuid is recorded so Master can persist the JSON blob.
func (m *S3Cow) Upload(ctx context.Context, snapshotID string) (*cow.RemoteUUIDs, error) {
	_ = ctx
	id := strings.TrimSpace(snapshotID)
	if id == "" {
		return nil, fmt.Errorf("snapshot_id is required")
	}
	uuids := &cow.RemoteUUIDs{}
	for _, ref := range activateObjectRefs(ctx, cow.BackendS3, id) {
		uuid, err := m.uploadOne(ref.Name)
		if err != nil {
			m.setUpload(id, cow.RemoteStateFailed, err.Error(), nil)
			return nil, fmt.Errorf("upload %s %s: %w", ref.Role, ref.Name, err)
		}
		switch ref.Role {
		case "rootfs":
			uuids.Rootfs = uuid
		case "memory":
			uuids.Memory = uuid
		}
	}
	if entry, err := GetLocalSnapshotFor(ctx, cow.BackendS3, id); err == nil && entry != nil {
		if meta := strings.TrimSpace(entry.MetaDir); meta != "" {
			uuids.Metadata = m.mockRemoteUUID(id + ":metadata")
		}
		entry.RemoteUUIDs = uuids
		_ = WriteSnapshotCatalogFor(cow.BackendS3, entry)
	}
	m.setUpload(id, cow.RemoteStateReady, "uploaded", uuids)
	return uuids, nil
}

func (m *S3Cow) uploadOne(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("object name is required")
	}
	if exp, ok := m.engine.(cowSnapshotExporter); ok {
		uuid, err := exp.ExportSnapshot(name)
		if err == nil && strings.TrimSpace(uuid) != "" {
			return uuid, nil
		}
		if err != nil && !isCowSemantic(err, cubecow.SemPreconditionFailed) {
			return "", err
		}
	}
	return m.mockRemoteUUID(name), nil
}

func (m *S3Cow) mockRemoteUUID(name string) string {
	sum := sha1.Sum([]byte("s3-remote:" + name))
	return "remote-" + hex.EncodeToString(sum[:12])
}

// UploadStatus implements [cow.Uploader].
// Real upload state will come from cubecow_get_volume_info; that field is
// not ready, so S3 always reports ready for CubeMaster.
func (m *S3Cow) UploadStatus(ctx context.Context, snapshotID string) (*cow.RemoteStatus, error) {
	id := strings.TrimSpace(snapshotID)
	if id == "" {
		return nil, fmt.Errorf("snapshot_id is required")
	}
	st := &cow.RemoteStatus{
		SnapshotID: id,
		State:      cow.RemoteStateReady,
		Message:    "mock: get_volume_info upload status not ready",
	}
	m.uploadLock.Lock()
	entry, ok := m.uploadStates[id]
	m.uploadLock.Unlock()
	if ok {
		st.RemoteUUIDs = entry.uuids
	} else if cat, err := GetLocalSnapshotFor(ctx, cow.BackendS3, id); err == nil && cat != nil && !cat.RemoteUUIDs.Empty() {
		st.RemoteUUIDs = cat.RemoteUUIDs
	}
	return st, nil
}

func (m *S3Cow) setUpload(id, state, message string, uuids *cow.RemoteUUIDs) {
	m.uploadLock.Lock()
	defer m.uploadLock.Unlock()
	if m.uploadStates == nil {
		m.uploadStates = make(map[string]s3UploadEntry)
	}
	m.uploadStates[id] = s3UploadEntry{state: state, message: message, uuids: uuids}
}

var _ cow.Uploader = (*S3Cow)(nil)
