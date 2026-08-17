// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package snapshot

import (
	"context"
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
	backend, err := cow.NormalizeBackend(req.GetBackend())
	if err != nil {
		return &api.StatusResponse{
			RequestId:  req.GetRequestId(),
			SnapshotId: req.GetSnapshotId(),
			Backend:    strings.TrimSpace(req.GetBackend()),
			State:      cow.SyncStateFailed,
			Message:    err.Error(),
		}, nil
	}
	rsp := &api.StatusResponse{
		RequestId:  req.GetRequestId(),
		SnapshotId: req.GetSnapshotId(),
		Backend:    backend,
		State:      cow.SyncStatePending,
	}
	if backend != cow.BackendS3 {
		rsp.Message = "xfs has no remote sync"
		return rsp, nil
	}
	st, err := storage.SnapshotSyncStatus(ctx, backend, req.GetSnapshotId())
	if err != nil {
		rsp.State = cow.SyncStateFailed
		rsp.Message = err.Error()
		return rsp, nil
	}
	if st != nil {
		rsp.SnapshotId = st.SnapshotID
		rsp.State = st.State
		rsp.Message = st.Message
		rsp.RemoteReady = st.State == cow.SyncStateReady
	}
	return rsp, nil
}
