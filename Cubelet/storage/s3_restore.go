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

// Activate implements [cow.Activator]. Mock: open cubecow objects that
// already exist locally. Missing objects fail — there is no remote ingest.
// Does not start a sandbox.
func (m *S3Cow) Activate(ctx context.Context, snapshotID string) error {
	return activateStoreObjects(ctx, m, cow.BackendS3, snapshotID)
}

var _ cow.Activator = (*S3Cow)(nil)

func activateStoreObjects(ctx context.Context, store cow.Store, backend, snapshotID string) error {
	id := strings.TrimSpace(snapshotID)
	if id == "" {
		return fmt.Errorf("snapshot_id is required")
	}
	refs := activateObjectRefs(ctx, backend, id)
	if len(refs) == 0 {
		return fmt.Errorf("%w: snapshot %s has no rootfs/memory objects", ErrCowObjectMissing, id)
	}
	for _, ref := range refs {
		info, err := store.GetVolumeInfo(ctx, ref.Name)
		if err != nil {
			return err
		}
		if info == nil {
			return fmt.Errorf("%w: snapshot %s object %s", ErrCowObjectMissing, id, ref.Name)
		}
		if _, err := store.ResolveDevPath(ctx, ref.Name, ref.Kind); err != nil {
			return fmt.Errorf("activate snapshot %s object %s: %w", id, ref.Name, err)
		}
	}
	return nil
}

func activateObjectRefs(ctx context.Context, backend, snapshotID string) []CowObjectRef {
	entry, err := GetLocalSnapshotFor(ctx, backend, snapshotID)
	if err == nil && entry != nil {
		refs := make([]CowObjectRef, 0, 2)
		if name := strings.TrimSpace(entry.RootfsVol); name != "" {
			kind := strings.TrimSpace(entry.RootfsKind)
			if kind == "" {
				kind = cowKindSnapshot
			}
			refs = append(refs, CowObjectRef{Name: name, Kind: kind, Role: "rootfs"})
		}
		if name := strings.TrimSpace(entry.MemoryVol); name != "" {
			kind := strings.TrimSpace(entry.MemoryKind)
			if kind == "" {
				kind = cowKindVolume
			}
			refs = append(refs, CowObjectRef{Name: name, Kind: kind, Role: "memory"})
		}
		if len(refs) > 0 {
			return refs
		}
	}
	return []CowObjectRef{
		{Name: cowTemplateRootfsName(snapshotID), Kind: cowKindSnapshot, Role: "rootfs"},
		{Name: cowTemplateMemoryName(snapshotID), Kind: cowKindVolume, Role: "memory"},
	}
}
