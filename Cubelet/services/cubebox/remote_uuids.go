// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package cubebox

import (
	"context"
	"fmt"

	"github.com/tencentcloud/CubeSandbox/Cubelet/storage"
	"github.com/tencentcloud/CubeSandbox/Cubelet/storage/cow"
)

// uploadRemoteUUIDsIfS3 runs cubecow_export_snapshot (via UploadSnapshot)
// before Pause / Commit / AppSnapshot returns. XFS is a no-op. S3 must
// produce a non-empty JSON blob of per-disk uuids for CubeMaster.
func uploadRemoteUUIDsIfS3(ctx context.Context, backend, snapshotID string) (string, error) {
	normalized, err := cow.NormalizeBackend(backend)
	if err != nil {
		return "", err
	}
	if normalized != cow.BackendS3 {
		return "", nil
	}
	uuids, err := storage.UploadSnapshot(ctx, normalized, snapshotID)
	if err != nil {
		return "", err
	}
	raw := uuids.JSON()
	if raw == "" {
		return "", fmt.Errorf("s3 cubecow_export_snapshot returned empty remote_uuids")
	}
	return raw, nil
}
