// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package snapshot

import (
	"context"
	"testing"

	api "github.com/tencentcloud/CubeSandbox/Cubelet/api/services/snapshot/v1"
	"github.com/tencentcloud/CubeSandbox/Cubelet/storage/cow"
)

func TestStatusXFSHasNoRemoteSync(t *testing.T) {
	t.Parallel()
	s := &service{}
	rsp, err := s.Status(context.Background(), &api.StatusRequest{
		RequestId:  "r1",
		SnapshotId: "snap-1",
		Backend:    cow.BackendXFS,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rsp.GetRemoteReady() {
		t.Fatal("xfs must not be remote_ready")
	}
	if rsp.GetState() != cow.SyncStatePending {
		t.Fatalf("state=%q", rsp.GetState())
	}
	if rsp.GetBackend() != cow.BackendXFS {
		t.Fatalf("backend=%q", rsp.GetBackend())
	}
}
