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

// Fetch implements [cow.Fetcher]. Cross-node recovery calls
// cubecow_import_lvol(name, export_uuid): the result is a RW volume
// derived from the remote snapshot, ready for Resume／sandbox create.
// activate=true opens the block device. Same-node: object already
// present → skip import.
func (m *S3Cow) Fetch(ctx context.Context, snapshotID string, uuids *cow.RemoteUUIDs, activate bool) error {
	id := strings.TrimSpace(snapshotID)
	if id == "" {
		return fmt.Errorf("snapshot_id is required")
	}
	if uuids.Empty() {
		return fmt.Errorf("remote_uuids is required for fetch")
	}
	refs := activateObjectRefs(ctx, cow.BackendS3, id)
	refs = appendMetadataFetchRef(refs, id, uuids)
	for _, ref := range refs {
		if IsS3MetadataBaseName(ref.Name) {
			continue
		}
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

// FetchAs imports remote objects under names the caller picks, taking the
// export uuid for each target from its Role.
//
// Cross-node sandboxes use this rather than [S3Cow.Fetch]: import_lvol hands
// back a RW volume, and a volume named after the sandbox lives and dies with
// it exactly like the clone a same-node create would have made. Importing
// under the package's own names instead gives one sandbox the node's only
// copy of a snapshot that later creates still need, and leaves nothing for
// destroy to key on.
func (m *S3Cow) FetchAs(ctx context.Context, targets []CowObjectRef, uuids *cow.RemoteUUIDs, activate bool) error {
	if uuids.Empty() {
		return fmt.Errorf("remote_uuids is required for fetch")
	}
	for _, target := range targets {
		name := strings.TrimSpace(target.Name)
		uuid := uuidForRole(uuids, target.Role)
		if name == "" || uuid == "" {
			continue
		}
		if IsS3MetadataBaseName(name) {
			return fmt.Errorf("refusing to import %s onto the node-local s3 metadata base", target.Role)
		}
		if err := m.fetchOne(ctx, name, uuid); err != nil {
			return fmt.Errorf("fetch %s %s: %w", target.Role, name, err)
		}
		if !activate {
			continue
		}
		if _, err := m.engine.ActivateVolume(name); err != nil && !isCowSemantic(err, cubecow.SemAlreadyExists) {
			return fmt.Errorf("activate fetched %s %s: %w", target.Role, name, err)
		}
	}
	return nil
}

func appendMetadataFetchRef(refs []CowObjectRef, snapshotID string, uuids *cow.RemoteUUIDs) []CowObjectRef {
	if uuids == nil || strings.TrimSpace(uuids.Metadata) == "" {
		return refs
	}
	for _, ref := range refs {
		if ref.Role == "metadata" {
			return refs
		}
	}
	// Exported metadata is the sealed snap name; import_lvol recreates a
	// RW volume under that same name for local Resume／mount.
	name := S3MetadataSnapshotName(snapshotID)
	if name == "" || IsS3MetadataBaseName(name) {
		return refs
	}
	return append(refs, CowObjectRef{Name: name, Kind: cowKindVolume, Role: "metadata"})
}

func (m *S3Cow) fetchOne(ctx context.Context, name, remoteUUID string) error {
	_ = ctx
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("object name is required")
	}
	if info, err := m.GetVolumeInfo(ctx, name); err == nil && info != nil {
		return nil
	}
	imp, ok := m.engine.(cowVolumeImporter)
	if !ok || imp == nil {
		if info, err := m.GetVolumeInfo(ctx, name); err == nil && info != nil {
			return nil
		}
		return fmt.Errorf("%w: %s (remote_uuid=%s)", ErrCowObjectMissing, name, remoteUUID)
	}
	if _, err := imp.ImportLvol(name, remoteUUID); err != nil && !isCowSemantic(err, cubecow.SemAlreadyExists) {
		if !isCowSemantic(err, cubecow.SemPreconditionFailed) {
			return err
		}
		if info, err := m.GetVolumeInfo(ctx, name); err == nil && info != nil {
			return nil
		}
		return fmt.Errorf("%w: %s (remote_uuid=%s)", ErrCowObjectMissing, name, remoteUUID)
	}
	return nil
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
