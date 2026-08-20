// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package storage

import (
	"context"
	"testing"

	"github.com/tencentcloud/CubeSandbox/Cubelet/storage/cow"
)

// XFS packages never leave their node, so a stray remote_uuids annotation
// must not drag the S3 import path into an XFS Create.
func TestEnsureRemotePackageLocalSkipsNonS3Backend(t *testing.T) {
	imported, err := EnsureRemotePackageLocal(context.Background(), cow.BackendXFS, "snap-1", false,
		&cow.RemoteUUIDs{Rootfs: "r1", Memory: "m1", Metadata: "d1"})
	if err != nil {
		t.Fatalf("xfs backend should be a no-op, got %v", err)
	}
	if imported {
		t.Fatal("xfs backend reported an import")
	}
}

// No uuids means Master had nothing to hand over, which is every same-node
// restore of a package exported before uploads existed.
func TestEnsureRemotePackageLocalSkipsWithoutUUIDs(t *testing.T) {
	for name, uuids := range map[string]*cow.RemoteUUIDs{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			imported, err := EnsureRemotePackageLocal(context.Background(), cow.BackendS3, "snap-1", false, uuids)
			if err != nil {
				t.Fatalf("expected a no-op, got %v", err)
			}
			if imported {
				t.Fatal("import ran without uuids to import from")
			}
		})
	}
}
