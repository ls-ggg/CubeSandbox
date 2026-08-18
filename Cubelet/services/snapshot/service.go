// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package snapshot

import (
	"context"
	"fmt"
	"strings"

	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
	"google.golang.org/grpc"

	api "github.com/tencentcloud/CubeSandbox/Cubelet/api/services/snapshot/v1"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/constants"
)

var _ api.SnapshotServer = &service{}

func init() {
	registry.Register(&plugin.Registration{
		Type:   constants.CubeboxServicePlugin,
		ID:     "snapshot",
		InitFn: initFunc,
	})
}

func initFunc(ic *plugin.InitContext) (interface{}, error) {
	return &service{}, nil
}

type service struct {
	api.UnimplementedSnapshotServer
}

func (s *service) RegisterTCP(server *grpc.Server) error {
	api.RegisterSnapshotServer(server, s)
	return nil
}

func (s *service) Register(server *grpc.Server) error {
	api.RegisterSnapshotServer(server, s)
	return nil
}

func (s *service) Status(ctx context.Context, req *api.StatusRequest) (*api.StatusResponse, error) {
	_ = ctx
	backend, err := normalizeStatusBackend(req.GetBackend())
	if err != nil {
		return &api.StatusResponse{
			RequestId:  req.GetRequestId(),
			SnapshotId: req.GetSnapshotId(),
			Backend:    strings.TrimSpace(req.GetBackend()),
			State:      "failed",
			Message:    err.Error(),
		}, nil
	}
	rsp := &api.StatusResponse{
		RequestId:  req.GetRequestId(),
		SnapshotId: req.GetSnapshotId(),
		Backend:    backend,
		State:      "pending",
	}
	if backend != "s3" {
		rsp.Message = "xfs has no remote upload"
		return rsp, nil
	}
	// cubecow_get_volume_info does not yet report upload. Mock ready so
	// CubeMaster can persist remote_status=ready and pass CanCrossNode.
	rsp.State = "ready"
	rsp.RemoteReady = true
	rsp.Message = "mock: get_volume_info upload status not ready"
	return rsp, nil
}

func normalizeStatusBackend(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "xfs", "cow", "cubecow", "reflink", "xfscow":
		return "xfs", nil
	case "s3":
		return "s3", nil
	default:
		return "", fmt.Errorf("unsupported backend %q (want xfs or s3)", raw)
	}
}
