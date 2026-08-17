// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package templatecenter

import (
	"fmt"
	"strings"

	"github.com/tencentcloud/CubeSandbox/CubeMaster/pkg/base/constants"
	sandboxtypes "github.com/tencentcloud/CubeSandbox/CubeMaster/pkg/service/sandbox/types"
)

func storageBackendFromCreate(req *sandboxtypes.CreateCubeSandboxReq) string {
	if req == nil {
		return ""
	}
	if b := strings.TrimSpace(req.Backend); b != "" {
		return b
	}
	if req.Annotations == nil {
		return ""
	}
	return strings.TrimSpace(req.Annotations[constants.CubeAnnotationStorageBackend])
}

// stampCreateRequestBackend writes a normalized xfs|s3 backend onto the
// create request and the Master→Cubelet annotation. Empty input is a no-op
// so callers that omit backend keep the historical request shape.
func stampCreateRequestBackend(req *sandboxtypes.CreateCubeSandboxReq, backend string) error {
	if req == nil || strings.TrimSpace(backend) == "" {
		return nil
	}
	normalized, err := constants.NormalizeSnapshotBackend(backend)
	if err != nil {
		return err
	}
	req.Backend = normalized
	if req.Annotations == nil {
		req.Annotations = map[string]string{}
	}
	req.Annotations[constants.CubeAnnotationStorageBackend] = normalized
	return nil
}

// applyStoredCreateBackend copies a stored template/snapshot backend onto
// req when the request itself does not name one. Missing or invalid stored
// values are ignored so old rows keep working.
func applyStoredCreateBackend(req *sandboxtypes.CreateCubeSandboxReq, stored string) error {
	if req == nil {
		return nil
	}
	if raw := storageBackendFromCreate(req); raw != "" {
		return stampCreateRequestBackend(req, raw)
	}
	if !isPinnedSnapshotBackend(stored) {
		return nil
	}
	if err := stampCreateRequestBackend(req, stored); err != nil {
		return nil
	}
	return nil
}

func isPinnedSnapshotBackend(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case constants.SnapshotBackendXFS, constants.SnapshotBackendS3:
		return true
	default:
		return false
	}
}

// pinnedCleanupBackend returns xfs|s3 only when the row was explicitly
// pinned. Empty / historical cubecow stay empty so Cleanup keeps the
// historical Cubelet default (xfs).
func pinnedCleanupBackend(raw string) string {
	if !isPinnedSnapshotBackend(raw) {
		return ""
	}
	normalized, err := constants.NormalizeSnapshotBackend(raw)
	if err != nil {
		return ""
	}
	return normalized
}

func cleanupBackendFromTargets(targets *templateCleanupTargets) string {
	if targets == nil {
		return ""
	}
	if targets.Snapshot != nil {
		if hint := pinnedCleanupBackend(targets.Snapshot.Backend); hint != "" {
			return hint
		}
	}
	if targets.Definition != nil {
		return pinnedCleanupBackend(targets.Definition.StorageBackend)
	}
	return ""
}

// InheritCreateBackendFromTemplate copies the template backend onto a
// sandbox create when the request omitted it. An explicit request backend
// that conflicts with a pinned template backend is rejected. Both empty
// leaves the request unchanged.
func InheritCreateBackendFromTemplate(req, templateReq *sandboxtypes.CreateCubeSandboxReq) error {
	if req == nil {
		return nil
	}
	requestRaw := storageBackendFromCreate(req)
	templateRaw := storageBackendFromCreate(templateReq)
	if requestRaw == "" {
		return stampCreateRequestBackend(req, templateRaw)
	}
	if templateRaw == "" {
		return stampCreateRequestBackend(req, requestRaw)
	}
	requested, err := constants.NormalizeSnapshotBackend(requestRaw)
	if err != nil {
		return err
	}
	templateBackend, err := constants.NormalizeSnapshotBackend(templateRaw)
	if err != nil {
		return stampCreateRequestBackend(req, requestRaw)
	}
	if requested != templateBackend {
		return fmt.Errorf("backend %q does not match template backend %q", requested, templateBackend)
	}
	return stampCreateRequestBackend(req, requested)
}
