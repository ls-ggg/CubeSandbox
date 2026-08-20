// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/cubecow"
)

func useTestCowStorage(t *testing.T, engine *fakeCowEngine) {
	t.Helper()
	previousLocalStorage := localStorage
	localStorage = &local{
		config:       &Config{StorageBackend: "cubecow"},
		cowManager:   &XfsCow{engine: engine},
		s3CowManager: &S3Cow{engine: engine},
	}
	t.Cleanup(func() {
		localStorage = previousLocalStorage
	})
}

func TestCleanupObjectsDispatchesByKind(t *testing.T) {
	engine := &fakeCowEngine{
		deleteSnapshotErr: &cubecow.CowError{Code: cubecow.SemNotFound, RawRC: int32(cubecow.SemNotFound)},
	}
	useTestCowStorage(t, engine)

	err := CleanupObjects(context.Background(), []CowObjectRef{
		{Name: "tpl-1-rootfs", Kind: CowKindSnapshot, Role: "rootfs"},
		{Name: "tpl-1-memory", Kind: CowKindVolume, Role: "memory"},
		{Name: "tpl-1-build-rootfs", Kind: CowKindVolume, Role: "build_rootfs"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"tpl-1-rootfs", "tpl-1-memory", "tpl-1-build-rootfs"}, engine.deactivatedVolumes)
	assert.Equal(t, []string{"tpl-1-rootfs"}, engine.deletedSnapshots)
	assert.Equal(t, []string{"tpl-1-memory", "tpl-1-build-rootfs"}, engine.deletedVolumes)
}

func TestCleanupObjectsStopsWhenRootfsSurvives(t *testing.T) {
	engine := &fakeCowEngine{
		deleteErrByName: map[string]error{
			"tpl-2-rootfs": errors.New("Device or resource busy"),
		},
	}
	useTestCowStorage(t, engine)

	// Memory listed first on purpose: the sweep must reorder, not trust the
	// caller, or a busy rootfs still loses its package.
	err := CleanupObjectsFor(context.Background(), "s3", []CowObjectRef{
		{Name: "tpl-2-memory", Kind: CowKindVolume, Role: "memory"},
		{Name: "tpl-2-rootfs", Kind: CowKindSnapshot, Role: "rootfs"},
		{Name: "s3-meta-tpl-2-snap", Kind: CowKindSnapshot, Role: "metadata"},
	})
	require.Error(t, err)
	assert.Equal(t, []string{"tpl-2-rootfs"}, engine.deletedSnapshots)
	assert.Empty(t, engine.deletedVolumes, "memory must survive a rootfs that refused to go")
}

func TestCleanupObjectsDeactivatesActivatedMemorySnap(t *testing.T) {
	engine := &fakeCowEngine{
		volumeInfos: map[string]*cubecow.Volume{
			"tpl-x-memory-snap": {DevicePath: "/dev/mapper/tpl-x-memory-snap", SizeBytes: 64 << 20},
		},
	}
	useTestCowStorage(t, engine)

	err := CleanupObjectsFor(context.Background(), "s3", []CowObjectRef{
		{Name: "tpl-x-memory-snap", Role: "memory"},
	})
	require.NoError(t, err)
	require.Contains(t, engine.deactivatedVolumes, "tpl-x-memory-snap")
	require.Contains(t, engine.deletedSnapshots, "tpl-x-memory-snap")
	require.Empty(t, engine.volumeInfos["tpl-x-memory-snap"].DevicePath)
}

func TestAppendS3SealedPackageCleanupRefsAddsMemorySnap(t *testing.T) {
	refs := AppendS3SealedPackageCleanupRefs("tpl-abc", []CowObjectRef{
		{Name: "custom-rootfs", Kind: CowKindSnapshot, Role: "rootfs"},
		{Name: "custom-mem", Kind: CowKindVolume, Role: "memory"},
	})
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	assert.Contains(t, names, "custom-rootfs")
	assert.Contains(t, names, "custom-mem")
	assert.Contains(t, names, "tpl-tpl-abc-memory-snap")
	assert.Contains(t, names, "s3-meta-tpl-abc-snap")
}

func TestInspectObjectsReportsExistingAndMissing(t *testing.T) {
	engine := &fakeCowEngine{
		volumeInfos: map[string]*cubecow.Volume{
			"tpl-1-rootfs": {DevicePath: "/dev/mapper/tpl-1-rootfs", SizeBytes: 4096},
		},
	}
	useTestCowStorage(t, engine)

	statuses, err := InspectObjects(context.Background(), []CowObjectRef{
		{Name: "tpl-1-rootfs", Kind: CowKindSnapshot, Role: "rootfs"},
		{Name: "tpl-1-memory", Kind: CowKindVolume, Role: "memory"},
	})
	require.NoError(t, err)
	require.Len(t, statuses, 2)
	assert.True(t, statuses[0].Exists)
	assert.Equal(t, "/dev/mapper/tpl-1-rootfs", statuses[0].DevicePath)
	assert.Equal(t, uint64(4096), statuses[0].SizeBytes)
	assert.False(t, statuses[1].Exists)
	assert.Empty(t, statuses[1].DevicePath)
}

func TestObjectMetricsValidatesRequiredKeys(t *testing.T) {
	engine := &fakeCowEngine{
		metrics: map[string]uint64{
			"total_bytes":  100,
			"used_bytes":   70,
			"volume_count": 4,
		},
	}
	useTestCowStorage(t, engine)

	metrics, err := ObjectMetrics(context.Background())
	require.Nil(t, metrics)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "snapshot_count")
}

func TestResolveRollbackRefsHonorsMemoryKindSnapshot(t *testing.T) {
	engine := &fakeCowEngine{
		volumeInfos: map[string]*cubecow.Volume{
			"tpl-1-rootfs": {DevicePath: "/dev/mapper/tpl-1-rootfs", SizeBytes: 4096},
			"tpl-1-memory": {DevicePath: "/dev/mapper/tpl-1-memory", SizeBytes: 8192},
		},
	}
	useTestCowStorage(t, engine)

	refs, err := ResolveRollbackRefs(context.Background(), "tpl-1-rootfs", "tpl-1-memory", CowKindSnapshot)
	require.NoError(t, err)
	require.NotNil(t, refs.Memory)
	assert.Equal(t, CowKindSnapshot, refs.Memory.Kind)
	assert.Equal(t, "/dev/mapper/tpl-1-memory", refs.Memory.DevPath)
}

func TestResolveRollbackRefsDefaultsMemoryKindToVolume(t *testing.T) {
	engine := &fakeCowEngine{
		volumeInfos: map[string]*cubecow.Volume{
			"tpl-1-rootfs": {DevicePath: "/dev/mapper/tpl-1-rootfs", SizeBytes: 4096},
			"tpl-1-memory": {DevicePath: "/dev/mapper/tpl-1-memory", SizeBytes: 8192},
		},
	}
	useTestCowStorage(t, engine)

	refs, err := ResolveRollbackRefs(context.Background(), "tpl-1-rootfs", "tpl-1-memory", "")
	require.NoError(t, err)
	require.NotNil(t, refs.Memory)
	assert.Equal(t, CowKindVolume, refs.Memory.Kind)
}

func TestCommitMemoryFromBaseProducesSnapshotObject(t *testing.T) {
	engine := &fakeCowEngine{
		createSnapshotPath: "/dev/mapper/tpl-snap-memory",
		volumeInfos: map[string]*cubecow.Volume{
			"tpl-snap-memory": {DevicePath: "/dev/mapper/tpl-snap-memory", SizeBytes: 4096},
		},
	}
	useTestCowStorage(t, engine)

	obj, err := CommitMemoryFromBase(context.Background(), &CowSnapshotObject{Name: "tpl-base-memory", Kind: CowKindVolume}, "snap", 4096)
	require.NoError(t, err)
	require.NotNil(t, obj)
	assert.Equal(t, "tpl-snap-memory", obj.Name)
	assert.Equal(t, CowKindSnapshot, obj.Kind)
	assert.Equal(t, "/dev/mapper/tpl-snap-memory", obj.DevPath)
	assert.Equal(t, uint64(4096), obj.SizeBytes)
	assert.Equal(t, [][2]string{{"tpl-base-memory", "tpl-snap-memory"}}, engine.createSnapshots)
}

func TestCommitMemoryFromBaseRejectsMissingSource(t *testing.T) {
	useTestCowStorage(t, &fakeCowEngine{})

	obj, err := CommitMemoryFromBase(context.Background(), nil, "snap", 4096)
	require.Error(t, err)
	assert.Nil(t, obj)
	assert.Contains(t, err.Error(), "source memory object is required")
}

func TestObjectMetricsSuccess(t *testing.T) {
	engine := &fakeCowEngine{
		metrics: map[string]uint64{
			"total_bytes":    100,
			"used_bytes":     70,
			"volume_count":   4,
			"snapshot_count": 3,
		},
	}
	useTestCowStorage(t, engine)

	metrics, err := ObjectMetrics(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(3), metrics["snapshot_count"])
}
