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
	"github.com/tencentcloud/CubeSandbox/Cubelet/storage"
	"github.com/tencentcloud/CubeSandbox/Cubelet/storage/cow"
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
	backend, err := normalizeStatusBackend(req.GetBackend())
	if err != nil {
		return &api.StatusResponse{
			RequestId:  req.GetRequestId(),
			SnapshotId: req.GetSnapshotId(),
			Backend:    strings.TrimSpace(req.GetBackend()),
			State:      cow.RemoteStateFailed,
			Message:    err.Error(),
		}, nil
	}
	rsp := &api.StatusResponse{
		RequestId:  req.GetRequestId(),
		SnapshotId: req.GetSnapshotId(),
		Backend:    backend,
		State:      cow.RemoteStatePending,
	}
	if backend != cow.BackendS3 {
		rsp.Message = "xfs has no remote upload"
		return rsp, nil
	}

	st, err := storage.SnapshotUploadStatus(ctx, backend, req.GetSnapshotId())
	if err != nil {
		rsp.State = cow.RemoteStateFailed
		rsp.Message = err.Error()
		return rsp, nil
	}
	if st == nil {
		rsp.Message = "empty upload status"
		return rsp, nil
	}
	rsp.State = strings.TrimSpace(st.State)
	if rsp.State == "" {
		rsp.State = cow.RemoteStatePending
	}
	rsp.Message = st.Message
	if st.RemoteUUIDs != nil && !st.RemoteUUIDs.Empty() && rsp.Message == "" {
		rsp.Message = st.RemoteUUIDs.JSON()
	}
	rsp.RemoteReady = rsp.State == cow.RemoteStateReady
	return rsp, nil
}

func normalizeStatusBackend(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "xfs", "cow", "cubecow", "reflink", "xfscow":
		return cow.BackendXFS, nil
	case "s3":
		return cow.BackendS3, nil
	default:
		return "", fmt.Errorf("unsupported backend %q (want xfs or s3)", raw)
	}
}
