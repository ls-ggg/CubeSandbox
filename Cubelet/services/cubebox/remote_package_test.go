// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package cubebox

import (
	"testing"

	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/constants"
)

func TestRemotePackageRefFromAnnotationsWithoutUUIDsIsNoRestore(t *testing.T) {
	cases := map[string]map[string]string{
		"no annotations": nil,
		"plain create":   {constants.MasterAnnotationStorageBackend: "s3"},
		"same-node xfs restore": {
			constants.MasterAnnotationRuntimeSnapshotID: "snap-1",
		},
		"snapshot id but empty uuids": {
			constants.MasterAnnotationRuntimeSnapshotID:   "snap-1",
			constants.MasterAnnotationSnapshotRemoteUUIDs: `{}`,
			constants.MasterAnnotationStorageBackend:      "s3",
		},
		"uuids but no snapshot id": {
			constants.MasterAnnotationSnapshotRemoteUUIDs: `{"rootfs":"r1"}`,
		},
	}
	for name, ann := range cases {
		t.Run(name, func(t *testing.T) {
			ref, err := remotePackageRefFromAnnotations(ann)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ref != nil {
				t.Fatalf("expected no restore ref, got %+v", ref)
			}
		})
	}
}

// A Resume sets both ids to the pause snapshot; the package it restores from
// lives under pause-snapshots, so the pause id has to win.
func TestRemotePackageRefFromAnnotationsPrefersPausePackage(t *testing.T) {
	ref, err := remotePackageRefFromAnnotations(map[string]string{
		constants.MasterAnnotationPauseSnapshotID:     "snap-pause",
		constants.MasterAnnotationRuntimeSnapshotID:   "snap-pause",
		constants.MasterAnnotationSnapshotRemoteUUIDs: `{"rootfs":"r1","memory":"m1","metadata":"d1"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref == nil {
		t.Fatal("expected a restore ref")
	}
	if ref.SnapshotID != "snap-pause" || !ref.Pause {
		t.Fatalf("got id=%q pause=%v, want snap-pause pause=true", ref.SnapshotID, ref.Pause)
	}
	if ref.UUIDs.Metadata != "d1" {
		t.Fatalf("metadata uuid = %q, want d1", ref.UUIDs.Metadata)
	}
}

// Master says the package is not on this node and hands over nothing to
// import it with. Failing here names the real problem; letting it through
// surfaces three steps later as a catalog miss that reads like corruption.
func TestRemotePackageRefFromAnnotationsRejectsCrossNodeWithoutUUIDs(t *testing.T) {
	_, err := remotePackageRefFromAnnotations(map[string]string{
		constants.MasterAnnotationRuntimeSnapshotID: "snap-1",
		constants.MasterAnnotationSnapshotCrossNode: "true",
		constants.MasterAnnotationStorageBackend:    "s3",
	})
	if err == nil {
		t.Fatal("expected an error for a cross-node restore with no remote_uuids")
	}
}

func TestRemotePackageRefFromAnnotationsCarriesCrossNodeVerdict(t *testing.T) {
	ref, err := remotePackageRefFromAnnotations(map[string]string{
		constants.MasterAnnotationRuntimeSnapshotID:   "snap-run",
		constants.MasterAnnotationSnapshotRemoteUUIDs: `{"rootfs":"r1","metadata":"d1"}`,
		constants.MasterAnnotationSnapshotCrossNode:   "true",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref == nil || !ref.CrossNode {
		t.Fatalf("expected a cross-node ref, got %+v", ref)
	}
}

func TestRemotePackageRefFromAnnotationsFromSnapshotIsNotPause(t *testing.T) {
	ref, err := remotePackageRefFromAnnotations(map[string]string{
		constants.MasterAnnotationRuntimeSnapshotID:   "snap-run",
		constants.MasterAnnotationSnapshotRemoteUUIDs: `{"rootfs":"r1"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref == nil {
		t.Fatal("expected a restore ref")
	}
	if ref.SnapshotID != "snap-run" || ref.Pause {
		t.Fatalf("got id=%q pause=%v, want snap-run pause=false", ref.SnapshotID, ref.Pause)
	}
}

func TestRemotePackageRefFromAnnotationsRejectsUnsafeID(t *testing.T) {
	if _, err := remotePackageRefFromAnnotations(map[string]string{
		constants.MasterAnnotationRuntimeSnapshotID:   "../../etc",
		constants.MasterAnnotationSnapshotRemoteUUIDs: `{"rootfs":"r1"}`,
	}); err == nil {
		t.Fatal("expected an error for a traversal snapshot id")
	}
}
