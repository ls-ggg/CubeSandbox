// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package cubebox

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tencentcloud/CubeSandbox/CubeMaster/pkg/base/constants"
	snapshotv1 "github.com/tencentcloud/CubeSandbox/Cubelet/api/services/snapshot/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const defaultCubeletGRPCPort = 9999

// Temporary ops aid shared by tpl list / snapshot list --s3; remove later.
const temporaryS3ListFlagUsage = "TEMP: query origin-node cubelet for rootfs snapshot deletable (will be removed in a later version)"

const (
	rootfsDeletableTimeout = 5 * time.Second
	rootfsDeletableUnknown = "unknown"
)

type rootfsDeletableQuery struct {
	SnapshotID string
	Backend    string
}

func cubeletGRPCPort(cPort int) int {
	if cPort <= 0 {
		return defaultCubeletGRPCPort
	}
	return cPort
}

func formatRootfsDeletable(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "yes", "1":
		return "true"
	case "false", "no", "0":
		return "false"
	case "":
		return "-"
	default:
		return raw
	}
}

func shouldQueryRootfsDeletable(backend string) bool {
	return constants.IsS3Backend(backend)
}

// queryRootfsDeletableByNodes groups queries by origin host IP and calls
// Cubelet Snapshot.BatchStatus once per node (5s timeout → "unknown").
func queryRootfsDeletableByNodes(port int, byNode map[string][]rootfsDeletableQuery) map[string]string {
	out := make(map[string]string)
	if len(byNode) == 0 {
		return out
	}
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for hostIP, items := range byNode {
		hostIP = strings.TrimSpace(hostIP)
		if hostIP == "" || len(items) == 0 {
			continue
		}
		hostIP, items := hostIP, items
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := batchRootfsDeletableOneNode(hostIP, port, items)
			mu.Lock()
			for id, v := range got {
				out[id] = v
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

func batchRootfsDeletableOneNode(hostIP string, port int, items []rootfsDeletableQuery) map[string]string {
	out := make(map[string]string, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.SnapshotID)
		if id == "" {
			continue
		}
		out[id] = rootfsDeletableUnknown
	}
	if len(out) == 0 {
		return out
	}

	queries := make([]*snapshotv1.StatusQuery, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.SnapshotID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		queries = append(queries, &snapshotv1.StatusQuery{
			SnapshotId: id,
			Backend:    strings.TrimSpace(item.Backend),
		})
	}

	addr := net.JoinHostPort(hostIP, strconv.Itoa(cubeletGRPCPort(port)))
	ctx, cancel := context.WithTimeout(context.Background(), rootfsDeletableTimeout)
	defer cancel()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return out
	}
	defer conn.Close()

	rsp, err := snapshotv1.NewSnapshotClient(conn).BatchStatus(ctx, &snapshotv1.BatchStatusRequest{
		RequestId: uuid.NewString(),
		Items:     queries,
	})
	if err != nil {
		// Timeout / dial / RPC failure → keep "unknown" for every id on this node.
		return out
	}
	if rsp == nil {
		return out
	}
	for _, item := range rsp.GetItems() {
		if item == nil {
			continue
		}
		id := strings.TrimSpace(item.GetSnapshotId())
		if id == "" {
			continue
		}
		out[id] = formatRootfsDeletable(item.GetRootfsDeletable())
	}
	return out
}

func firstReplicaNodeIP(replicas []replicaStatus) string {
	for _, r := range replicas {
		if ip := strings.TrimSpace(r.NodeIP); ip != "" {
			return ip
		}
	}
	return ""
}
