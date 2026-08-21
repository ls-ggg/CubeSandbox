// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package storage

import (
	"context"
	"os"
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
	require.Contains(t, engine.deactivatedVolumes, S3MetadataBaseVolumeName)
	require.Empty(t, engine.volumeInfos[S3MetadataBaseVolumeName].DevicePath)

	require.NoError(t, EnsureS3MetadataBase(context.Background()))
	require.Equal(t, 1, formatted)
	require.Len(t, engine.createVolumes, 1)
	require.Empty(t, engine.volumeInfos[S3MetadataBaseVolumeName].DevicePath)
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

// No uuid yet means the export has not been recorded anywhere, so an empty
// status is cubecow having nothing to report on rather than a lost export.
func TestUploadStatusEmptyExportStatusWithoutUUIDIsRunning(t *testing.T) {
	engine := &fakeCowEngine{
		volumeInfos: map[string]*cubecow.Volume{
			"tpl-snap-1-rootfs": {SizeBytes: 1 << 20},
			"tpl-snap-1-memory": {SizeBytes: 64 << 20},
		},
	}
	useTestCowStorage(t, engine)

	st, err := SnapshotUploadStatus(context.Background(), cow.BackendS3, "snap-1")
	require.NoError(t, err)
	require.Equal(t, cow.RemoteStateRunning, st.State)
}

// s3lvol returns a uuid before it can tell whether the transfer will run,
// and on failure records nothing under it: every later lookup then says no
// such export and cubecow leaves the status empty. Reporting that as still
// uploading leaves the package inprogress forever, because only a fresh
// export can ever change the answer.
func TestUploadStatusDeadExportUUIDIsFailed(t *testing.T) {
	engine := &fakeCowEngine{
		volumeInfos: map[string]*cubecow.Volume{
			"tpl-snap-1-rootfs": {
				SizeBytes:    1 << 20,
				ExportUUID:   "uuid-root",
				ExportStatus: cow.ExportStatusDone,
			},
			"tpl-snap-1-memory": {SizeBytes: 64 << 20, ExportUUID: "uuid-mem"},
		},
	}
	useTestCowStorage(t, engine)

	st, err := SnapshotUploadStatus(context.Background(), cow.BackendS3, "snap-1")
	require.NoError(t, err)
	require.Equal(t, cow.RemoteStateFailed, st.State)
	// Name the dead uuid: re-exporting is the only fix, and the operator
	// needs to know which role to re-export.
	require.Contains(t, st.Message, "uuid-mem")
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
	require.Contains(t, engine.deletedVolumes, S3MetadataVolumeName("snap-1"))
	require.Contains(t, engine.deactivatedVolumes, S3MetadataVolumeName("snap-1"))
	require.Contains(t, engine.deactivatedVolumes, S3MetadataSnapshotName("snap-1"))
	require.NotContains(t, engine.activatedVolumes, S3MetadataSnapshotName("snap-1"))

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

func TestUploadTemplateRootfsExportsOnlyRootfs(t *testing.T) {
	stubS3MetadataMounts(t)
	engine := &fakeCowEngine{
		volumeInfos: map[string]*cubecow.Volume{
			"tpl-tpl-1-rootfs":              {SizeBytes: 1 << 20},
			"tpl-tpl-1-memory-snap":         {SizeBytes: 64 << 20},
			S3MetadataSnapshotName("tpl-1"): {SizeBytes: 8 << 20},
		},
	}
	useTestCowStorage(t, engine)
	home := t.TempDir()
	require.NoError(t, EnsureSnapshotPackage(cow.BackendS3, home))
	require.NoError(t, WriteSnapshotCatalogFor(cow.BackendS3, &SnapshotCatalogEntry{
		SnapshotID:   "tpl-1",
		SnapshotPath: home,
		MetaDir:      filepath.Join(home, SnapshotMetadataDir),
		RootfsVol:    "tpl-tpl-1-rootfs",
		RootfsKind:   cowKindSnapshot,
		MemoryVol:    "tpl-tpl-1-memory-snap",
		MemoryKind:   cowKindSnapshot,
		MetadataVol:  S3MetadataSnapshotName("tpl-1"),
		MetadataKind: cowKindSnapshot,
		Kind:         CatalogKindTemplate,
		Backend:      cow.BackendS3,
	}))

	uuid, err := UploadTemplateRootfs(context.Background(), cow.BackendS3, "tpl-1")
	require.NoError(t, err)
	require.NotEmpty(t, uuid)
	require.Contains(t, engine.exportSnapshots, "tpl-tpl-1-rootfs")
	require.NotContains(t, engine.exportSnapshots, "tpl-tpl-1-memory-snap")
	require.NotContains(t, engine.exportSnapshots, S3MetadataSnapshotName("tpl-1"))

	// The id belongs on the object, not in catalog.json: by export time the
	// package is sealed and its catalog is read-only.
	raw, err := os.ReadFile(filepath.Join(home, SnapshotMetadataDir, snapshotCatalogFileName))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "remote_uuids")
}

func TestUploadTemplateRootfsIsNoOpOnXFS(t *testing.T) {
	useTestCowStorage(t, &fakeCowEngine{})
	uuid, err := UploadTemplateRootfs(context.Background(), cow.BackendXFS, "tpl-1")
	require.NoError(t, err)
	require.Empty(t, uuid)
}

func TestS3CowEmptyDevicePathAfterCreateFails(t *testing.T) {
	engine := &fakeCowEngine{createVolumePath: ""}
	store := &S3Cow{engine: engine}

	_, _, err := store.createOrResolveVolumePath(context.Background(), "vol-empty", 8<<20)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty device_path")
	require.Contains(t, err.Error(), "s3lvol/NVMe-oF")

	_, err = store.createTemplateVolumePath("tpl-empty", 8<<20)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty device_path")
}

func TestS3ResolveDevPathAlwaysActivatesForLatestPath(t *testing.T) {
	engine := &fakeCowEngine{
		volumeInfos: map[string]*cubecow.Volume{
			"tpl-x-memory-snap": {DevicePath: "/dev/nvme17n1", SizeBytes: 64 << 20},
		},
		activatePaths: map[string]string{
			"tpl-x-memory-snap": "/dev/nvme3n1",
		},
	}
	store := &S3Cow{engine: engine}
	devPath, err := store.ResolveDevPath(context.Background(), "tpl-x-memory-snap", cowKindSnapshot)
	require.NoError(t, err)
	require.Equal(t, "/dev/nvme3n1", devPath)
	require.Equal(t, []string{"tpl-x-memory-snap"}, engine.activatedVolumes)
	require.Equal(t, "/dev/nvme3n1", engine.volumeInfos["tpl-x-memory-snap"].DevicePath)
}

func TestS3CowEmptyDevicePathAfterActivateFails(t *testing.T) {
	engine := &fakeCowEngine{
		volumeInfos: map[string]*cubecow.Volume{
			"vol-1": {DevicePath: "", SizeBytes: 8 << 20},
		},
		activatePaths: map[string]string{
			"vol-1": "",
		},
	}
	store := &S3Cow{engine: engine}
	_, err := store.ResolveDevPath(context.Background(), "vol-1", cowKindVolume)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty device_path")
	require.Contains(t, err.Error(), "ActivateVolume")
	require.Contains(t, err.Error(), "refuse mkfs/mount")
}

func TestEnsureS3MetadataBaseFailsOnEmptyDevicePath(t *testing.T) {
	stubS3MetadataMounts(t)
	engine := &fakeCowEngine{createVolumePath: ""}
	useTestCowStorage(t, engine)

	err := EnsureS3MetadataBase(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty device_path")
	require.NotContains(t, engine.createVolumes, "") // still attempted create
	require.Equal(t, []string{S3MetadataBaseVolumeName}, engine.createVolumes)
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

func TestEnsureS3MetadataBaseDeactivatesAlreadyActiveLeftover(t *testing.T) {
	stubS3MetadataMounts(t)
	engine := &fakeCowEngine{
		volumeInfos: map[string]*cubecow.Volume{
			S3MetadataBaseVolumeName:   {SizeBytes: 8 << 20, DevicePath: "/dev/mapper/" + S3MetadataBaseVolumeName},
			S3MetadataBaseSnapshotName: {SizeBytes: 8 << 20, DevicePath: "/dev/mapper/" + S3MetadataBaseSnapshotName},
		},
	}
	useTestCowStorage(t, engine)

	require.NoError(t, EnsureS3MetadataBase(context.Background()))
	require.Empty(t, engine.createVolumes)
	require.Contains(t, engine.deactivatedVolumes, S3MetadataBaseVolumeName)
	require.Contains(t, engine.deactivatedVolumes, S3MetadataBaseSnapshotName)
	require.Empty(t, engine.volumeInfos[S3MetadataBaseVolumeName].DevicePath)
	require.Empty(t, engine.volumeInfos[S3MetadataBaseSnapshotName].DevicePath)
}

func TestRemountS3MetadataDeactivatesSealedAlreadyActiveLeftovers(t *testing.T) {
	stubS3MetadataMounts(t)
	id := "tpl-leak-1"
	vol := S3MetadataVolumeName(id)
	snap := S3MetadataSnapshotName(id)
	mem := "tpl-" + id + "-memory-snap"
	engine := &fakeCowEngine{
		volumeInfos: map[string]*cubecow.Volume{
			S3MetadataBaseVolumeName:   {SizeBytes: 8 << 20, DevicePath: "/dev/mapper/" + S3MetadataBaseVolumeName},
			S3MetadataBaseSnapshotName: {SizeBytes: 8 << 20},
			vol:                        {SizeBytes: 8 << 20, DevicePath: "/dev/mapper/" + vol},
			snap:                       {SizeBytes: 8 << 20, DevicePath: "/dev/mapper/" + snap},
			mem:                        {SizeBytes: 64 << 20, DevicePath: "/dev/mapper/" + mem},
		},
	}
	useTestCowStorage(t, engine)

	home := t.TempDir()
	require.NoError(t, EnsureSnapshotPackage(cow.BackendS3, home))
	require.NoError(t, WriteSnapshotCatalogFor(cow.BackendS3, &SnapshotCatalogEntry{
		SnapshotID:   id,
		SnapshotPath: home,
		MetaDir:      filepath.Join(home, SnapshotMetadataDir),
		RootfsVol:    "tpl-" + id + "-rootfs",
		RootfsKind:   cowKindSnapshot,
		MemoryVol:    mem,
		MemoryKind:   cowKindSnapshot,
		MetadataVol:  snap,
		MetadataKind: cowKindSnapshot,
		Backend:      cow.BackendS3,
	}))
	t.Cleanup(func() { DeleteSnapshotCatalogFor(cow.BackendS3, id) })

	s3MetadataMu.Lock()
	require.NoError(t, persistDerivedLocked(id, vol, filepath.Join(home, SnapshotMetadataDir)))
	s3MetadataMu.Unlock()

	require.NoError(t, RemountS3MetadataVolumes(context.Background()))
	require.Empty(t, engine.createVolumeFromSnapshots)
	require.Contains(t, engine.deactivatedVolumes, vol)
	require.Contains(t, engine.deactivatedVolumes, snap)
	require.Contains(t, engine.deactivatedVolumes, mem)
	require.NotContains(t, engine.activatedVolumes, vol)
	require.NotContains(t, engine.activatedVolumes, snap)
	require.Empty(t, engine.volumeInfos[vol].DevicePath)
	require.Empty(t, engine.volumeInfos[snap].DevicePath)
	require.Empty(t, engine.volumeInfos[mem].DevicePath)
}

func TestRemountS3MetadataDeactivatesHostPackageDirWithoutCatalog(t *testing.T) {
	stubS3MetadataMounts(t)
	id := "tpl-7b70df45c3294788aa3dbbdb"
	vol := S3MetadataVolumeName(id)
	snap := S3MetadataSnapshotName(id)
	mem := cowTemplateMemoryName(id) + "-snap"
	engine := &fakeCowEngine{
		volumeInfos: map[string]*cubecow.Volume{
			S3MetadataBaseVolumeName:   {SizeBytes: 8 << 20, DevicePath: "/dev/mapper/" + S3MetadataBaseVolumeName},
			S3MetadataBaseSnapshotName: {SizeBytes: 8 << 20},
			vol:                        {SizeBytes: 8 << 20, DevicePath: "/dev/mapper/" + vol},
			snap:                       {SizeBytes: 8 << 20, DevicePath: "/dev/mapper/" + snap},
			mem:                        {SizeBytes: 64 << 20, DevicePath: "/dev/mapper/" + mem},
		},
	}
	useTestCowStorage(t, engine)
	work := t.TempDir()
	localStorage.config.DataPath = work

	home := SnapshotHome(cow.BackendS3, SnapshotKindNormal, id)
	require.NoError(t, EnsureSnapshotPackage(cow.BackendS3, home))

	require.NoError(t, RemountS3MetadataVolumes(context.Background()))
	require.Empty(t, engine.createVolumeFromSnapshots)
	require.Contains(t, engine.deactivatedVolumes, vol)
	require.Contains(t, engine.deactivatedVolumes, snap)
	require.Contains(t, engine.deactivatedVolumes, mem)
	require.NotContains(t, engine.activatedVolumes, vol)
	require.NotContains(t, engine.activatedVolumes, snap)
	require.Empty(t, engine.volumeInfos[vol].DevicePath)
	require.Empty(t, engine.volumeInfos[snap].DevicePath)
	require.Empty(t, engine.volumeInfos[mem].DevicePath)
}
