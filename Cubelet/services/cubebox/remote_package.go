// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package cubebox

import (
	"context"
	"fmt"
	"strings"

	"github.com/tencentcloud/CubeSandbox/Cubelet/api/services/cubebox/v1"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/constants"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/log"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/pathutil"
	"github.com/tencentcloud/CubeSandbox/Cubelet/storage"
	"github.com/tencentcloud/CubeSandbox/Cubelet/storage/cow"
)

// isCrossNodeRestore reports Master's verdict that it placed this restore on
// a node holding no replica of the package. Only Master can know this, so
// Cubelet treats it as fact rather than something to infer.
func isCrossNodeRestore(ann map[string]string) bool {
	return strings.EqualFold(strings.TrimSpace(ann[constants.MasterAnnotationSnapshotCrossNode]), "true")
}

// remotePackageRef names the package a Create restores from, as seen from a
// node that may never have held it.
type remotePackageRef struct {
	SnapshotID string
	Pause      bool
	CrossNode  bool
	UUIDs      *cow.RemoteUUIDs
}

// remotePackageRefFromAnnotations reads the restore source out of a Create.
// Master fills remote_uuids for every S3 restore, same-node ones included, so
// a ref here only says which package to look for — not that an import is due.
func remotePackageRefFromAnnotations(ann map[string]string) (*remotePackageRef, error) {
	if len(ann) == 0 {
		return nil, nil
	}
	// Same precedence as CreateContext.GetSnapshotTemplateID: a Resume sets
	// both ids to the pause snapshot and restores from the pause package.
	pauseID := strings.TrimSpace(ann[constants.MasterAnnotationPauseSnapshotID])
	id := pauseID
	if id == "" {
		id = strings.TrimSpace(ann[constants.MasterAnnotationRuntimeSnapshotID])
	}
	if id == "" {
		id = strings.TrimSpace(ann[constants.MasterAnnotationAppSnapshotTemplateID])
	}
	if id == "" {
		return nil, nil
	}
	crossNode := isCrossNodeRestore(ann)
	uuids := cow.ParseRemoteUUIDs(ann[constants.MasterAnnotationSnapshotRemoteUUIDs])
	if uuids.Empty() {
		// Without uuids there is nothing to import, and Master already said
		// the package is not here. Say so now instead of failing three steps
		// later on a catalog miss that looks like corruption.
		if crossNode {
			return nil, fmt.Errorf("cross-node restore of %s carries no remote_uuids", id)
		}
		return nil, nil
	}
	if err := pathutil.ValidateSafeID(id); err != nil {
		return nil, fmt.Errorf("invalid restore snapshot id: %w", err)
	}
	return &remotePackageRef{SnapshotID: id, Pause: pauseID != "", CrossNode: crossNode, UUIDs: uuids}, nil
}

// ensureRemotePackageLocal imports the restore source before the rest of
// Create goes looking for it in the local catalog. Cross-node Resume and
// cross-node create-from-snapshot both land on a node where nothing about the
// package exists yet, and the first read (pause sandbox_spec, memory volume
// resolve, rootfs attach) would otherwise fail as a plain catalog miss.
//
// The import happens once. A package already here — a same-node Resume, a
// second Resume onto the node that imported it, or a retry after a failed
// Create — is used in place.
func ensureRemotePackageLocal(ctx context.Context, req *cubebox.RunCubeSandboxRequest) error {
	ann := req.GetAnnotations()
	ref, err := remotePackageRefFromAnnotations(ann)
	if err != nil || ref == nil {
		return err
	}
	backend, err := storageBackendFromAnnotations(ann)
	if err != nil {
		return fmt.Errorf("restore snapshot backend: %w", err)
	}
	imported, err := storage.EnsureRemotePackageLocal(ctx, backend, ref.SnapshotID, ref.Pause, ref.UUIDs)
	if err != nil {
		return err
	}
	switch {
	case imported:
		log.G(ctx).Infof("restore %s: imported package from remote storage (cross_node=%v)",
			ref.SnapshotID, ref.CrossNode)
	case ref.CrossNode:
		log.G(ctx).Infof("restore %s: package already on this node, reusing it", ref.SnapshotID)
	}
	return nil
}
