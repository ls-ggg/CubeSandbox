// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package templatecenter

import (
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
