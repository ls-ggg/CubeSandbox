// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package templatecenter

import (
	"testing"

	"github.com/tencentcloud/CubeSandbox/CubeMaster/pkg/base/constants"
	"github.com/tencentcloud/CubeSandbox/CubeMaster/pkg/base/db/models"
	sandboxtypes "github.com/tencentcloud/CubeSandbox/CubeMaster/pkg/service/sandbox/types"
)

func TestStampCreateRequestBackendEmptyIsNoop(t *testing.T) {
	req := &sandboxtypes.CreateCubeSandboxReq{Annotations: map[string]string{"keep": "1"}}
	if err := stampCreateRequestBackend(req, ""); err != nil {
		t.Fatal(err)
	}
	if req.Backend != "" {
		t.Fatalf("Backend=%q, want empty", req.Backend)
	}
	if _, ok := req.Annotations[constants.CubeAnnotationStorageBackend]; ok {
		t.Fatal("empty backend must not inject storage annotation")
	}
	if req.Annotations["keep"] != "1" {
		t.Fatal("unrelated annotation was changed")
	}
}

func TestInheritCreateBackendFromTemplateBothEmpty(t *testing.T) {
	req := &sandboxtypes.CreateCubeSandboxReq{Annotations: map[string]string{}}
	if err := InheritCreateBackendFromTemplate(req, &sandboxtypes.CreateCubeSandboxReq{}); err != nil {
		t.Fatal(err)
	}
	if req.Backend != "" {
		t.Fatalf("Backend=%q, want empty so historical create stays unchanged", req.Backend)
	}
}

func TestInheritCreateBackendFromTemplateCopiesWhenRequestOmits(t *testing.T) {
	req := &sandboxtypes.CreateCubeSandboxReq{Annotations: map[string]string{}}
	tpl := &sandboxtypes.CreateCubeSandboxReq{Backend: constants.SnapshotBackendS3}
	if err := InheritCreateBackendFromTemplate(req, tpl); err != nil {
		t.Fatal(err)
	}
	if req.Backend != constants.SnapshotBackendS3 {
		t.Fatalf("Backend=%q, want s3", req.Backend)
	}
}

func TestPinnedCleanupBackendIgnoresHistoricalAndEmpty(t *testing.T) {
	if got := pinnedCleanupBackend(""); got != "" {
		t.Fatalf("empty cleanup backend=%q", got)
	}
	if got := pinnedCleanupBackend("cubecow"); got != "" {
		t.Fatalf("historical cubecow cleanup backend=%q", got)
	}
	if got := pinnedCleanupBackend(constants.SnapshotBackendS3); got != constants.SnapshotBackendS3 {
		t.Fatalf("s3 cleanup backend=%q", got)
	}
}

func TestCleanupBackendFromTargetsPrefersSnapshotPin(t *testing.T) {
	got := cleanupBackendFromTargets(&templateCleanupTargets{
		Snapshot:   &models.SnapshotRecord{Backend: constants.SnapshotBackendS3},
		Definition: &models.TemplateDefinition{StorageBackend: constants.SnapshotBackendXFS},
	})
	if got != constants.SnapshotBackendS3 {
		t.Fatalf("cleanup backend=%q, want s3 from snapshot row", got)
	}
	if got := cleanupBackendFromTargets(&templateCleanupTargets{
		Definition: &models.TemplateDefinition{StorageBackend: "cubecow"},
	}); got != "" {
		t.Fatalf("historical definition must stay empty, got %q", got)
	}
}

func TestApplyStoredCreateBackendIgnoresHistoricalCubecow(t *testing.T) {
	req := &sandboxtypes.CreateCubeSandboxReq{Annotations: map[string]string{}}
	if err := applyStoredCreateBackend(req, "cubecow"); err != nil {
		t.Fatal(err)
	}
	if req.Backend != "" {
		t.Fatalf("historical cubecow must not pin Backend, got %q", req.Backend)
	}
}

func TestInheritCreateBackendFromTemplateRejectsConflict(t *testing.T) {
	req := &sandboxtypes.CreateCubeSandboxReq{Backend: constants.SnapshotBackendS3}
	tpl := &sandboxtypes.CreateCubeSandboxReq{Backend: constants.SnapshotBackendXFS}
	if err := InheritCreateBackendFromTemplate(req, tpl); err == nil {
		t.Fatal("expected conflict error")
	}
}
