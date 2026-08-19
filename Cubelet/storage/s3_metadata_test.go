// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package storage

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/cubecow"
	"github.com/tencentcloud/CubeSandbox/Cubelet/storage/cow"
)

type memS3MetadataKV struct {
	mu   sync.Mutex
	data []byte
}

func (m *memS3MetadataKV) Get() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.data) == 0 {
		return nil, nil
	}
	out := make([]byte, len(m.data))
	copy(out, m.data)
	return out, nil
}

func (m *memS3MetadataKV) Set(body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = append([]byte(nil), body...)
	return nil
}

func stubS3MetadataMounts(t *testing.T) {
	t.Helper()
	mounted := map[string]string{}
	origFormat := formatS3MetadataBaseDevice
	origMount := mountS3MetadataDevice
	origUmount := unmountS3MetadataPath
	origIsMounted := s3MetadataIsMounted
	origKV := testS3MetadataKV
	kv := &memS3MetadataKV{}
	testS3MetadataKV = kv
	formatS3MetadataBaseDevice = func(string) error {
		return nil
	}
	mountS3MetadataDevice = func(devicePath, mountPath string) error {
		mounted[mountPath] = devicePath
		return nil
	}
	unmountS3MetadataPath = func(mountPath string) error {
		delete(mounted, mountPath)
		return nil
	}
	s3MetadataIsMounted = func(mountPath string) bool {
		_, ok := mounted[mountPath]
		return ok
	}
	t.Cleanup(func() {
		formatS3MetadataBaseDevice = origFormat
		mountS3MetadataDevice = origMount
		unmountS3MetadataPath = origUmount
		s3MetadataIsMounted = origIsMounted
		testS3MetadataKV = origKV
		s3MetadataMu.Lock()
		s3MetadataMounts = map[string]string{}
		s3MetadataMu.Unlock()
	})
}

func TestEnsureS3MetadataBaseCreatesOnce(t *testing.T) {
	stubS3MetadataMounts(t)
	formatted := 0
	formatS3MetadataBaseDevice = func(string) error {
		formatted++
		return nil
	}
	engine := &fakeCowEngine{createVolumePath: "/dev/mapper/" + S3MetadataBaseVolumeName}
	useTestCowStorage(t, engine)

	require.NoError(t, EnsureS3MetadataBase(context.Background()))
	require.Equal(t, 1, formatted)
	require.Equal(t, []string{S3MetadataBaseVolumeName}, engine.createVolumes)
	require.Equal(t, uint64(s3MetadataBaseSizeBytes), engine.createVolumeSizes[S3MetadataBaseVolumeName])

	require.NoError(t, EnsureS3MetadataBase(context.Background()))
	require.Equal(t, 1, formatted)
	require.Len(t, engine.createVolumes, 1)
}

func TestEnsureS3MetadataBaseRecreatesWhenMissing(t *testing.T) {
	stubS3MetadataMounts(t)
	formatted := 0
	formatS3MetadataBaseDevice = func(string) error {
		formatted++
		return nil
	}
	engine := &fakeCowEngine{createVolumePath: "/dev/mapper/" + S3MetadataBaseVolumeName}
	useTestCowStorage(t, engine)

	require.NoError(t, EnsureS3MetadataBase(context.Background()))
	require.Equal(t, 1, formatted)
	delete(engine.volumeInfos, S3MetadataBaseVolumeName)

	require.NoError(t, EnsureS3MetadataBase(context.Background()))
	require.Equal(t, 2, formatted)
	require.Len(t, engine.createVolumes, 2)
}

func TestPrepareS3MetadataMountSnapshotsFromBase(t *testing.T) {
	stubS3MetadataMounts(t)
	engine := &fakeCowEngine{createVolumePath: "/dev/mapper/" + S3MetadataBaseVolumeName}
	useTestCowStorage(t, engine)

	mount := t.TempDir()
	require.NoError(t, PrepareS3MetadataMount(context.Background(), cow.BackendS3, "snap-meta-1", mount))
	require.Equal(t, [][2]string{{S3MetadataBaseVolumeName, S3MetadataBaseSnapshotName}}, engine.createSnapshots)
	require.Equal(t, [][2]string{{S3MetadataBaseSnapshotName, S3MetadataVolumeName("snap-meta-1")}}, engine.createVolumeFromSnapshots)
	require.True(t, s3MetadataIsMounted(mount))

	require.NoError(t, UnmountS3Metadata(mount))
	final := t.TempDir()
	require.NoError(t, MountS3MetadataAt(context.Background(), cow.BackendS3, "snap-meta-1", final))
	require.True(t, s3MetadataIsMounted(final))
	require.False(t, s3MetadataIsMounted(mount))

	require.NoError(t, ReleaseS3MetadataVolume(context.Background(), cow.BackendS3, "snap-meta-1"))
	require.Equal(t, []string{S3MetadataVolumeName("snap-meta-1")}, engine.deletedVolumes)
	require.NotContains(t, engine.deletedVolumes, S3MetadataBaseVolumeName)
	require.NotContains(t, engine.deletedSnapshots, S3MetadataBaseSnapshotName)
	require.False(t, s3MetadataIsMounted(final))
}

func TestMountS3MetadataAtClonesSealedSnap(t *testing.T) {
	stubS3MetadataMounts(t)
	id := "pause-1"
	snap := S3MetadataSnapshotName(id)
	engine := &fakeCowEngine{
		volumeInfos: map[string]*cubecow.Volume{
			S3MetadataBaseVolumeName:   {SizeBytes: 8 << 20, DevicePath: "/dev/mapper/" + S3MetadataBaseVolumeName},
			S3MetadataBaseSnapshotName: {SizeBytes: 8 << 20},
			snap:                       {SizeBytes: 8 << 20},
		},
	}
	useTestCowStorage(t, engine)

	mount := t.TempDir()
	require.NoError(t, MountS3MetadataAt(context.Background(), cow.BackendS3, id, mount))
	require.Equal(t, [][2]string{{snap, S3MetadataVolumeName(id)}}, engine.createVolumeFromSnapshots)
	require.True(t, s3MetadataIsMounted(mount))
	require.NotContains(t, engine.activatedVolumes, snap)
}

func TestPrepareS3MetadataMountNoOpOnXFS(t *testing.T) {
	stubS3MetadataMounts(t)
	engine := &fakeCowEngine{}
	useTestCowStorage(t, engine)

	require.NoError(t, PrepareS3MetadataMount(context.Background(), cow.BackendXFS, "snap-xfs", t.TempDir()))
	require.Empty(t, engine.createSnapshots)
	require.Empty(t, engine.createVolumes)
}

func TestCloneS3MetadataFromParentUsesParentVolume(t *testing.T) {
	stubS3MetadataMounts(t)
	parentSnap := S3MetadataSnapshotName("tpl-1")
	engine := &fakeCowEngine{
		volumeInfos: map[string]*cubecow.Volume{
			S3MetadataBaseVolumeName:   {SizeBytes: 8 << 20, DevicePath: "/dev/mapper/" + S3MetadataBaseVolumeName},
			S3MetadataBaseSnapshotName: {SizeBytes: 8 << 20, DevicePath: ""},
			parentSnap:                 {SizeBytes: 8 << 20, DevicePath: "/dev/mapper/" + parentSnap},
		},
	}
	useTestCowStorage(t, engine)

	mount := t.TempDir()
	require.NoError(t, CloneS3MetadataFromParent(context.Background(), cow.BackendS3, "tpl-1", "sb-1", mount))
	require.Equal(t, [][2]string{{parentSnap, S3MetadataVolumeName("sb-1")}}, engine.createVolumeFromSnapshots)
	require.True(t, s3MetadataIsMounted(mount))
}

func TestCloneS3MetadataFromParentFallsBackToBase(t *testing.T) {
	stubS3MetadataMounts(t)
	engine := &fakeCowEngine{
		volumeInfos: map[string]*cubecow.Volume{
			S3MetadataBaseVolumeName: {SizeBytes: 8 << 20, DevicePath: "/dev/mapper/" + S3MetadataBaseVolumeName},
		},
	}
	useTestCowStorage(t, engine)

	mount := t.TempDir()
	require.NoError(t, CloneS3MetadataFromParent(context.Background(), cow.BackendS3, "tpl-missing", "sb-2", mount))
	require.Contains(t, engine.createSnapshots, [2]string{S3MetadataBaseVolumeName, S3MetadataBaseSnapshotName})
	require.Equal(t, [][2]string{{S3MetadataBaseSnapshotName, S3MetadataVolumeName("sb-2")}}, engine.createVolumeFromSnapshots)
}

func TestUploadStatusUsesExportStatus(t *testing.T) {
	engine := &fakeCowEngine{
		volumeInfos: map[string]*cubecow.Volume{
			"tpl-snap-1-rootfs": {
				SizeBytes:    1 << 20,
				ExportUUID:   "uuid-root",
				ExportStatus: cow.ExportStatusDone,
			},
			"tpl-snap-1-memory": {
				SizeBytes:    64 << 20,
				ExportUUID:   "uuid-mem",
				ExportStatus: cow.ExportStatusInProgress,
			},
		},
	}
	useTestCowStorage(t, engine)

	st, err := SnapshotUploadStatus(context.Background(), cow.BackendS3, "snap-1")
	require.NoError(t, err)
	require.Equal(t, cow.RemoteStateRunning, st.State)
	require.Equal(t, "uuid-root", st.RemoteUUIDs.Rootfs)
	require.Equal(t, "uuid-mem", st.RemoteUUIDs.Memory)

	engine.volumeInfos["tpl-snap-1-memory"].ExportStatus = cow.ExportStatusDone
	st, err = SnapshotUploadStatus(context.Background(), cow.BackendS3, "snap-1")
	require.NoError(t, err)
	require.Equal(t, cow.RemoteStateReady, st.State)
}

func TestUploadStatusEmptyExportStatusIsRunning(t *testing.T) {
	engine := &fakeCowEngine{
		volumeInfos: map[string]*cubecow.Volume{
			"tpl-snap-1-rootfs": {SizeBytes: 1 << 20, ExportUUID: "uuid-root"},
			"tpl-snap-1-memory": {SizeBytes: 64 << 20, ExportUUID: "uuid-mem"},
		},
	}
	useTestCowStorage(t, engine)

	st, err := SnapshotUploadStatus(context.Background(), cow.BackendS3, "snap-1")
	require.NoError(t, err)
	require.Equal(t, cow.RemoteStateRunning, st.State)
}

func TestUploadSkipsMetadataBaseAndUploadsDerived(t *testing.T) {
	stubS3MetadataMounts(t)
	engine := &fakeCowEngine{
		volumeInfos: map[string]*cubecow.Volume{
			"tpl-snap-1-rootfs":            {SizeBytes: 1 << 20},
			"tpl-snap-1-memory":            {SizeBytes: 64 << 20},
			S3MetadataVolumeName("snap-1"): {SizeBytes: 8 << 20},
			S3MetadataBaseVolumeName:       {SizeBytes: 8 << 20},
		},
	}
	useTestCowStorage(t, engine)
	home := t.TempDir()
	require.NoError(t, EnsureSnapshotPackage(cow.BackendS3, home))
	require.NoError(t, WriteSnapshotCatalogFor(cow.BackendS3, &SnapshotCatalogEntry{
		SnapshotID:   "snap-1",
		SnapshotPath: home,
		MetaDir:      filepath.Join(home, SnapshotMetadataDir),
		RootfsVol:    "tpl-snap-1-rootfs",
		RootfsKind:   cowKindSnapshot,
		MemoryVol:    "tpl-snap-1-memory",
		MemoryKind:   cowKindVolume,
		MetadataVol:  S3MetadataVolumeName("snap-1"),
		MetadataKind: cowKindVolume,
		Backend:      cow.BackendS3,
	}))
	require.NoError(t, FinalizeS3PackageSnapshots(context.Background(), cow.BackendS3, "snap-1"))

	uuids, err := UploadSnapshot(context.Background(), cow.BackendS3, "snap-1")
	require.NoError(t, err)
	require.NotEmpty(t, uuids.Rootfs)
	require.NotEmpty(t, uuids.Memory)
	require.NotEmpty(t, uuids.Metadata)
	require.NotEqual(t, uuids.Rootfs, uuids.Metadata)
	require.NotContains(t, engine.exportSnapshots, S3MetadataBaseVolumeName)
	require.Contains(t, engine.exportSnapshots, S3MetadataSnapshotName("snap-1"))
	require.NotContains(t, engine.exportSnapshots, S3MetadataVolumeName("snap-1"))

	_, err = (&S3Cow{engine: engine}).uploadOne(S3MetadataBaseVolumeName)
	require.Error(t, err)
	require.Contains(t, err.Error(), "node-local s3 metadata base")
}

func TestIsS3MetadataBaseName(t *testing.T) {
	require.True(t, IsS3MetadataBaseName(S3MetadataBaseVolumeName))
	require.True(t, IsS3MetadataBaseName(S3MetadataBaseSnapshotName))
	require.False(t, IsS3MetadataBaseName(S3MetadataVolumeName("snap-1")))
	require.Equal(t, "s3-meta-snap-1", S3MetadataVolumeName("snap-1"))
	require.Equal(t, "", S3MetadataCatalogVol(cow.BackendXFS, "snap-1"))
	require.Equal(t, "s3-meta-snap-1", S3MetadataCatalogVol(cow.BackendS3, "snap-1"))
	require.Equal(t, cowKindVolume, S3MetadataCatalogKind(cow.BackendS3))
	require.Equal(t, "", S3MetadataCatalogKind(cow.BackendXFS))
}
