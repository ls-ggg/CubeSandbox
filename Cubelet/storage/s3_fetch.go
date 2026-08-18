// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/cubecow"
	"github.com/tencentcloud/CubeSandbox/Cubelet/storage/cow"
)

// Fetch implements [cow.Fetcher]. Cross-node use calls cubecow_import_lvol
// first. activate=true then cubecow_activate_volume (fetch may already
// open the device). Same-node / mock: objects already exist → skip fetch,
// only activate when asked.
func (m *S3Cow) Fetch(ctx context.Context, snapshotID string, uuids *cow.RemoteUUIDs, activate bool) error {
	id := strings.TrimSpace(snapshotID)
	if id == "" {
		return fmt.Errorf("snapshot_id is required")
	}
	if uuids.Empty() {
		return fmt.Errorf("remote_uuids is required for fetch")
	}
	refs := activateObjectRefs(ctx, cow.BackendS3, id)
	for _, ref := range refs {
		uuid := uuidForRole(uuids, ref.Role)
		if uuid == "" {
			continue
		}
		if err := m.fetchOne(ctx, ref.Name, uuid); err != nil {
			return fmt.Errorf("fetch %s %s: %w", ref.Role, ref.Name, err)
		}
		if activate {
			if _, err := m.engine.ActivateVolume(ref.Name); err != nil && !isCowSemantic(err, cubecow.SemAlreadyExists) {
				return fmt.Errorf("activate fetched %s %s: %w", ref.Role, ref.Name, err)
			}
		}
	}
	return nil
}

func (m *S3Cow) fetchOne(ctx context.Context, name, remoteUUID string) error {
	if info, err := m.GetVolumeInfo(ctx, name); err == nil && info != nil {
		return nil
	}
	if imp, ok := m.engine.(cowVolumeImporter); ok {
		_, err := imp.ImportLvol(name, remoteUUID)
		if err == nil || isCowSemantic(err, cubecow.SemAlreadyExists) {
			return nil
		}
		if !isCowSemantic(err, cubecow.SemPreconditionFailed) {
			return err
		}
	}
	if info, err := m.GetVolumeInfo(ctx, name); err == nil && info != nil {
		return nil
	}
	return fmt.Errorf("%w: %s (remote_uuid=%s)", ErrCowObjectMissing, name, remoteUUID)
}

func uuidForRole(uuids *cow.RemoteUUIDs, role string) string {
	if uuids == nil {
		return ""
	}
	switch role {
	case "rootfs":
		return strings.TrimSpace(uuids.Rootfs)
	case "memory":
		return strings.TrimSpace(uuids.Memory)
	case "metadata":
		return strings.TrimSpace(uuids.Metadata)
	default:
		return ""
	}
}

var _ cow.Fetcher = (*S3Cow)(nil)
