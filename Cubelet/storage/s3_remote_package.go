// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tencentcloud/CubeSandbox/Cubelet/storage/cow"
)

// IsPackageLocal reports whether this node can already resolve the package,
// either from its catalog or from the cubecow objects the id names. This is
// the question "do I still have to import?" — after a completed import the
// answer is yes and the package is used in place.
func IsPackageLocal(ctx context.Context, backend, snapshotID string) (bool, error) {
	id := strings.TrimSpace(snapshotID)
	if id == "" {
		return false, nil
	}
	if _, err := GetLocalSnapshotFor(ctx, backend, id); err == nil {
		return true, nil
	} else if !errors.Is(err, ErrSnapshotCatalogNotFound) {
		return false, err
	}
	return false, nil
}

// EnsureRemotePackageLocal makes a template／snapshot／pause package readable
// on a node that does not have it, so everything downstream of Create keeps
// reading the local catalog exactly the way it does on the origin node.
// It reports whether an import actually happened.
//
// Only the metadata disk is imported, and it stands for the package itself:
// catalog.json, sandbox_spec.json and the kernel／image config all live on it.
// The rootfs and memory are not fetched here — each sandbox imports its own
// private copy of those (see [SandboxRootfsName], [SandboxMemoryName],
// [ImportS3MetadataForSandbox]), so that one sandbox cannot consume the
// node's only copy of a snapshot other creates still need.
//
// Re-entrant on every level: a package that is already here returns early, and
// a retry after a partial import skips what cubecow already has. Same node
// Resume carries the same remote_uuids and imports nothing.
func EnsureRemotePackageLocal(ctx context.Context, backend, snapshotID string, pause bool, uuids *cow.RemoteUUIDs) (bool, error) {
	if !isS3CatalogBackend(backend) {
		return false, nil
	}
	id := strings.TrimSpace(snapshotID)
	if id == "" || uuids.Empty() {
		return false, nil
	}
	local, err := IsPackageLocal(ctx, cow.BackendS3, id)
	if err != nil || local {
		return false, err
	}

	kind := SnapshotKindNormal
	if pause {
		kind = SnapshotKindPause
	}
	home := SnapshotHome(cow.BackendS3, kind, id)
	// The kind root the package lands under is what tells a pause package
	// apart from a runtime snapshot once the catalog is gone again.
	if err := EnsureSnapshotPackage(cow.BackendS3, home); err != nil {
		return false, fmt.Errorf("ensure remote package dirs for %s: %w", id, err)
	}
	metaOnly := &cow.RemoteUUIDs{Metadata: strings.TrimSpace(uuids.Metadata)}
	if metaOnly.Empty() {
		// Packages exported before metadata was part of the payload: nothing
		// to mount, and the catalog falls back to id-derived names.
		return false, nil
	}
	if err := FetchSnapshot(ctx, cow.BackendS3, id, metaOnly, false); err != nil {
		return false, fmt.Errorf("fetch remote metadata for %s: %w", id, err)
	}
	if err := MountS3MetadataAt(ctx, cow.BackendS3, id, filepath.Join(home, SnapshotMetadataDir)); err != nil {
		return false, fmt.Errorf("mount remote metadata for %s: %w", id, err)
	}
	return true, nil
}
