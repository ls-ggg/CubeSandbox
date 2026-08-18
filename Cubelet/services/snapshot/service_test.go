// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package snapshot

import (
	"context"
	"testing"

	api "github.com/tencentcloud/CubeSandbox/Cubelet/api/services/snapshot/v1"
)

func TestStatusXFSHasNoRemoteUpload(t *testing.T) {
	t.Parallel()
	s := &service{}
	rsp, err := s.Status(context.Background(), &api.StatusRequest{
		RequestId:  "r1",
		SnapshotId: "snap-1",
		Backend:    "xfs",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rsp.GetRemoteReady() {
		t.Fatal("xfs must not be remote_ready")
	}
	if rsp.GetState() != "pending" {
		t.Fatalf("state=%q", rsp.GetState())
	}
	if rsp.GetBackend() != "xfs" {
		t.Fatalf("backend=%q", rsp.GetBackend())
	}
}

func TestStatusS3MocksReadyForMaster(t *testing.T) {
	t.Parallel()
	s := &service{}
	rsp, err := s.Status(context.Background(), &api.StatusRequest{
		RequestId:  "r1",
		SnapshotId: "snap-1",
		Backend:    "s3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rsp.GetBackend() != "s3" {
		t.Fatalf("backend=%q", rsp.GetBackend())
	}
	if rsp.GetState() != "ready" {
		t.Fatalf("state=%q", rsp.GetState())
	}
	if !rsp.GetRemoteReady() {
		t.Fatal("s3 mock must be remote_ready")
	}
}
