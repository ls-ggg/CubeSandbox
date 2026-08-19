// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package storage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/cubecow"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/pathutil"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/utils"
	"github.com/tencentcloud/CubeSandbox/Cubelet/storage/cow"
	CubeLog "github.com/tencentcloud/CubeSandbox/cubelog"
)

const (
	// S3MetadataBaseVolumeName is the per-node, local-only cubecow volume
	// that Cubelet formats as ext4 during S3 init. Every later S3 metadata
	// disk (template / pause / snapshot / sandbox restore package) is a
	// snapshot of this volume (or of a parent package metadata disk).
	S3MetadataBaseVolumeName = "cubelet-s3-metadata-base"
	s3MetadataBaseSizeBytes  = 8 << 20
	s3MetadataVolumePrefix   = "s3-meta-"
	s3MetadataBucket         = "s3-metadata/v1"
	s3MetadataStateKey       = "state"
)

type s3MetadataBaseRecord struct {
	Name      string `json:"name"`
	SizeBytes uint64 `json:"size_bytes"`
	Formatted bool   `json:"formatted"`
}

type s3MetadataDerivedRecord struct {
	SnapshotID string `json:"snapshot_id"`
	VolName    string `json:"vol_name"`
	MountPath  string `json:"mount_path,omitempty"`
}

type s3MetadataState struct {
	Base    s3MetadataBaseRecord               `json:"base"`
	Derived map[string]s3MetadataDerivedRecord `json:"derived,omitempty"`
}

type s3MetadataKV interface {
	Get() ([]byte, error)
	Set([]byte) error
}

var (
	formatS3MetadataBaseDevice = formatS3MetadataBaseDeviceImpl
	mountS3MetadataDevice      = mountS3MetadataDeviceImpl
	unmountS3MetadataPath      = unmountS3MetadataPathImpl
	s3MetadataIsMounted        = s3MetadataIsMountedImpl

	s3MetadataMu     sync.Mutex
	s3MetadataMounts = map[string]string{} // snapshotID → current mount path
	testS3MetadataKV s3MetadataKV
)

// S3MetadataVolumeName is the cubecow snapshot cloned from the node-local
// metadata base for one template / pause / snapshot package.
func S3MetadataVolumeName(snapshotID string) string {
	id := strings.TrimSpace(snapshotID)
	if id == "" {
		return ""
	}
	return s3MetadataVolumePrefix + id
}

// IsS3MetadataBaseName reports the node-local metadata base. That volume
// must never be exported or fetched; it stays on this Cubelet.
func IsS3MetadataBaseName(name string) bool {
	return strings.TrimSpace(name) == S3MetadataBaseVolumeName
}

// S3MetadataCatalogVol is the catalog metadata_vol field for an S3 package.
func S3MetadataCatalogVol(backend, snapshotID string) string {
	if !isS3CatalogBackend(backend) {
		return ""
	}
	return S3MetadataVolumeName(snapshotID)
}

// S3MetadataCatalogKind is snapshot for S3 metadata disks, empty on XFS.
func S3MetadataCatalogKind(backend string) string {
	if !isS3CatalogBackend(backend) {
		return ""
	}
	return cowKindSnapshot
}

func parseS3MetadataSnapshotID(volName string) string {
	name := strings.TrimSpace(volName)
	if !strings.HasPrefix(name, s3MetadataVolumePrefix) {
		return ""
	}
	return strings.TrimPrefix(name, s3MetadataVolumePrefix)
}

func cowObjectPresent(info *cubecow.Volume, err error) (bool, error) {
	if err != nil {
		if isCowSemantic(err, cubecow.SemNotFound) {
			return false, nil
		}
		return false, err
	}
	return info != nil, nil
}

func requireS3Cow() (*S3Cow, error) {
	if localStorage == nil || localStorage.s3CowManager == nil {
		return nil, nil
	}
	store, ok := localStorage.s3CowManager.(*S3Cow)
	if !ok || store == nil {
		return nil, fmt.Errorf("s3 cow store is not *S3Cow")
	}
	return store, nil
}

// EnsureS3MetadataReady creates (or reconciles) the node-local 8MiB
// metadata base and remounts derived metadata snapshots after restart.
func EnsureS3MetadataReady(ctx context.Context) error {
	if err := EnsureS3MetadataBase(ctx); err != nil {
		return err
	}
	return RemountS3MetadataVolumes(ctx)
}

// EnsureS3MetadataBase creates the 8MiB base volume and mkfs.ext4's it on
// first use. If cubecow still has the volume after Cubelet / machine restart,
// it is reused and not formatted again. Missing volumes are recreated.
func EnsureS3MetadataBase(ctx context.Context) error {
	store, err := requireS3Cow()
	if err != nil {
		return err
	}
	if store == nil {
		return nil
	}

	s3MetadataMu.Lock()
	defer s3MetadataMu.Unlock()

	state, err := loadS3MetadataState()
	if err != nil {
		return err
	}
	name := strings.TrimSpace(state.Base.Name)
	if name == "" {
		name = S3MetadataBaseVolumeName
	}
	if name != S3MetadataBaseVolumeName {
		CubeLog.Warnf("s3 metadata base using persisted name %q", name)
	}

	info, infoErr := store.GetVolumeInfo(ctx, name)
	if isCowSemantic(infoErr, cubecow.ErrClosed) {
		CubeLog.Warnf("s3 metadata base skipped: s3 cubecow engine is closed")
		return nil
	}
	exists, err := cowObjectPresent(info, infoErr)
	if err != nil {
		return fmt.Errorf("lookup s3 metadata base %s: %w", name, err)
	}
	if exists {
		state.Base.Name = name
		state.Base.SizeBytes = s3MetadataBaseSizeBytes
		if info != nil && info.SizeBytes > 0 {
			state.Base.SizeBytes = info.SizeBytes
		}
		state.Base.Formatted = true
		if err := saveS3MetadataState(state); err != nil {
			return err
		}
		CubeLog.Infof("s3 metadata base %s already present; skip mkfs", name)
		return nil
	}

	devPath, created, err := store.createOrResolveVolumePath(ctx, name, s3MetadataBaseSizeBytes)
	if err != nil {
		return fmt.Errorf("create s3 metadata base %s: %w", name, err)
	}
	if created || !state.Base.Formatted {
		if err := formatS3MetadataBaseDevice(devPath); err != nil {
			_ = store.DeleteByKind(ctx, name, cowKindVolume)
			return fmt.Errorf("format s3 metadata base %s at %s: %w", name, devPath, err)
		}
	}
	state.Base = s3MetadataBaseRecord{
		Name:      name,
		SizeBytes: s3MetadataBaseSizeBytes,
		Formatted: true,
	}
	if err := saveS3MetadataState(state); err != nil {
		return err
	}
	CubeLog.Infof("s3 metadata base %s ready size=%d formatted=%v created=%v", name, s3MetadataBaseSizeBytes, true, created)
	return nil
}

// PrepareS3MetadataMount clones the node-local base into a package-specific
// snapshot and mounts it at mountPath (the designed metadata/ directory).
// XFS is a no-op.
func PrepareS3MetadataMount(ctx context.Context, backend, snapshotID, mountPath string) error {
	if !isS3CatalogBackend(backend) {
		return nil
	}
	id := strings.TrimSpace(snapshotID)
	mountPath = strings.TrimSpace(mountPath)
	if id == "" || mountPath == "" {
		return fmt.Errorf("s3 metadata snapshot_id and mount path are required")
	}
	if err := EnsureS3MetadataBase(ctx); err != nil {
		return err
	}
	store, err := requireS3Cow()
	if err != nil {
		return err
	}
	if store == nil {
		return fmt.Errorf("s3 cow store is not initialized")
	}

	s3MetadataMu.Lock()
	defer s3MetadataMu.Unlock()

	vol, err := store.deriveMetadataSnapshotLocked(ctx, id, "")
	if err != nil {
		return err
	}
	if err := mountS3MetadataLocked(id, vol.FilePath, mountPath); err != nil {
		return err
	}
	return persistDerivedLocked(id, vol.VolumeName, mountPath)
}

// CloneS3MetadataFromParent creates the child package／sandbox metadata disk
// as a snapshot of the parent's metadata volume (template / snapshot /
// pause). If the parent volume is missing, falls back to cloning the
// node-local base. Resume should call MountS3MetadataAt on the pause id
// instead — that disk already exists from Pause.
func CloneS3MetadataFromParent(ctx context.Context, backend, parentID, childID, mountPath string) error {
	if !isS3CatalogBackend(backend) {
		return nil
	}
	parentID = strings.TrimSpace(parentID)
	childID = strings.TrimSpace(childID)
	mountPath = strings.TrimSpace(mountPath)
	if childID == "" || mountPath == "" {
		return fmt.Errorf("s3 metadata child id and mount path are required")
	}
	if err := EnsureS3MetadataBase(ctx); err != nil {
		return err
	}
	store, err := requireS3Cow()
	if err != nil {
		return err
	}
	if store == nil {
		return fmt.Errorf("s3 cow store is not initialized")
	}

	s3MetadataMu.Lock()
	defer s3MetadataMu.Unlock()

	source := ""
	if parentID != "" && parentID != childID {
		parentVol := S3MetadataVolumeName(parentID)
		info, infoErr := store.GetVolumeInfo(ctx, parentVol)
		exists, err := cowObjectPresent(info, infoErr)
		if err != nil {
			return fmt.Errorf("lookup parent s3 metadata %s: %w", parentVol, err)
		}
		if exists {
			source = parentVol
		}
	}
	vol, err := store.deriveMetadataSnapshotLocked(ctx, childID, source)
	if err != nil {
		return err
	}
	if err := mountS3MetadataLocked(childID, vol.FilePath, mountPath); err != nil {
		return err
	}
	return persistDerivedLocked(childID, vol.VolumeName, mountPath)
}

// MountS3MetadataAt mounts an existing derived metadata snapshot at mountPath.
func MountS3MetadataAt(ctx context.Context, backend, snapshotID, mountPath string) error {
	if !isS3CatalogBackend(backend) {
		return nil
	}
	id := strings.TrimSpace(snapshotID)
	mountPath = strings.TrimSpace(mountPath)
	if id == "" || mountPath == "" {
		return fmt.Errorf("s3 metadata snapshot_id and mount path are required")
	}
	store, err := requireS3Cow()
	if err != nil {
		return err
	}
	if store == nil {
		return nil
	}

	s3MetadataMu.Lock()
	defer s3MetadataMu.Unlock()

	name := S3MetadataVolumeName(id)
	info, infoErr := store.GetVolumeInfo(ctx, name)
	exists, err := cowObjectPresent(info, infoErr)
	if err != nil {
		return fmt.Errorf("lookup s3 metadata snapshot %s: %w", name, err)
	}
	if !exists {
		return nil
	}
	devPath, err := store.ResolveDevPath(ctx, name, cowKindSnapshot)
	if err != nil {
		return fmt.Errorf("resolve s3 metadata snapshot %s: %w", name, err)
	}
	if err := mountS3MetadataLocked(id, devPath, mountPath); err != nil {
		return err
	}
	return persistDerivedLocked(id, name, mountPath)
}

// MountS3MetadataForSnapshot remounts a package's metadata disk at its
// catalog MetaDir, or at the on-disk snapshot home if the catalog is missing.
func MountS3MetadataForSnapshot(ctx context.Context, backend, snapshotID string) error {
	if !isS3CatalogBackend(backend) {
		return nil
	}
	id := strings.TrimSpace(snapshotID)
	if id == "" {
		return nil
	}
	store, err := requireS3Cow()
	if err != nil {
		return err
	}
	if store == nil {
		return nil
	}
	name := S3MetadataVolumeName(id)
	info, infoErr := store.GetVolumeInfo(ctx, name)
	exists, err := cowObjectPresent(info, infoErr)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	mountPath := s3MetadataMountPathFor(id)
	if mountPath == "" {
		return nil
	}
	return MountS3MetadataAt(ctx, backend, id, mountPath)
}

func s3MetadataMountPathFor(snapshotID string) string {
	if entry, err := GetLocalSnapshotFor(context.Background(), cow.BackendS3, snapshotID); err == nil && entry != nil {
		if p := strings.TrimSpace(entry.MetaDir); p != "" {
			return p
		}
		if home := strings.TrimSpace(entry.SnapshotPath); home != "" {
			return filepath.Join(home, SnapshotMetadataDir)
		}
	}
	s3MetadataMu.Lock()
	recPath := ""
	if state, err := loadS3MetadataState(); err == nil && state != nil && state.Derived != nil {
		if rec, ok := state.Derived[snapshotID]; ok {
			recPath = strings.TrimSpace(rec.MountPath)
		}
	}
	if p := s3MetadataMounts[snapshotID]; p != "" {
		recPath = p
	}
	s3MetadataMu.Unlock()
	if recPath != "" {
		return recPath
	}
	for _, kind := range []string{SnapshotKindPause, SnapshotKindNormal} {
		home := SnapshotHome(cow.BackendS3, kind, snapshotID)
		if home == "" {
			continue
		}
		if _, err := os.Stat(home); err == nil {
			return filepath.Join(home, SnapshotMetadataDir)
		}
	}
	return SnapshotMetaDir(cow.BackendS3, SnapshotKindNormal, snapshotID)
}

// UnmountS3Metadata unmounts a metadata mount point if it is mounted.
func UnmountS3Metadata(mountPath string) error {
	mountPath = strings.TrimSpace(mountPath)
	if mountPath == "" {
		return nil
	}
	s3MetadataMu.Lock()
	defer s3MetadataMu.Unlock()
	return unmountS3MetadataPathIfMounted(mountPath)
}

// ReleaseS3MetadataVolume unmounts and deletes the derived metadata snapshot.
// The node-local base is never deleted.
func ReleaseS3MetadataVolume(ctx context.Context, backend, snapshotID string) error {
	if !isS3CatalogBackend(backend) {
		return nil
	}
	id := strings.TrimSpace(snapshotID)
	if id == "" {
		return nil
	}
	name := S3MetadataVolumeName(id)
	if IsS3MetadataBaseName(name) {
		return nil
	}

	s3MetadataMu.Lock()
	defer s3MetadataMu.Unlock()

	var umountErr error
	if p := s3MetadataMounts[id]; p != "" {
		umountErr = unmountS3MetadataPathIfMounted(p)
	}
	if state, err := loadS3MetadataState(); err == nil && state != nil && state.Derived != nil {
		if rec, ok := state.Derived[id]; ok {
			if p := strings.TrimSpace(rec.MountPath); p != "" {
				if err := unmountS3MetadataPathIfMounted(p); err != nil {
					umountErr = errors.Join(umountErr, err)
				}
			}
		}
	}
	for _, kind := range []string{SnapshotKindPause, SnapshotKindNormal} {
		meta := SnapshotMetaDir(cow.BackendS3, kind, id)
		if err := unmountS3MetadataPathIfMounted(meta); err != nil {
			umountErr = errors.Join(umountErr, err)
		}
	}

	store, err := requireS3Cow()
	if err != nil {
		return errors.Join(umountErr, err)
	}
	if store != nil {
		if err := store.DeleteByKind(ctx, name, cowKindSnapshot); err != nil {
			umountErr = errors.Join(umountErr, fmt.Errorf("delete s3 metadata snapshot %s: %w", name, err))
		}
	}
	delete(s3MetadataMounts, id)
	if state, err := loadS3MetadataState(); err == nil && state != nil {
		if state.Derived != nil {
			delete(state.Derived, id)
		}
		if saveErr := saveS3MetadataState(state); saveErr != nil {
			umountErr = errors.Join(umountErr, saveErr)
		}
	}
	return umountErr
}

// RemountS3MetadataVolumes remounts derived metadata snapshots after Cubelet
// restart. Missing cubecow objects are skipped (the package is gone).
func RemountS3MetadataVolumes(ctx context.Context) error {
	store, err := requireS3Cow()
	if err != nil {
		return err
	}
	if store == nil {
		return nil
	}
	if _, err := store.GetVolumeInfo(ctx, S3MetadataBaseVolumeName); isCowSemantic(err, cubecow.ErrClosed) {
		return nil
	}

	s3MetadataMu.Lock()
	defer s3MetadataMu.Unlock()

	state, err := loadS3MetadataState()
	if err != nil {
		return err
	}
	seen := map[string]struct{}{}
	var remountErr error
	mountOne := func(id, mountPath string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		name := S3MetadataVolumeName(id)
		if IsS3MetadataBaseName(name) {
			return
		}
		info, infoErr := store.GetVolumeInfo(ctx, name)
		exists, err := cowObjectPresent(info, infoErr)
		if err != nil {
			remountErr = errors.Join(remountErr, err)
			return
		}
		if !exists {
			return
		}
		if strings.TrimSpace(mountPath) == "" {
			mountPath = s3MetadataGuessMountPathLocked(id)
		}
		if strings.TrimSpace(mountPath) == "" {
			return
		}
		devPath, err := store.ResolveDevPath(ctx, name, cowKindSnapshot)
		if err != nil {
			CubeLog.Warnf("s3 metadata remount %s: resolve: %v", name, err)
			return
		}
		if err := mountS3MetadataLocked(id, devPath, mountPath); err != nil {
			CubeLog.Warnf("s3 metadata remount %s at %s: %v", name, mountPath, err)
			return
		}
		_ = persistDerivedLocked(id, name, mountPath)
	}

	if state != nil && state.Derived != nil {
		for id, rec := range state.Derived {
			mountOne(id, rec.MountPath)
		}
	}
	baseName := S3MetadataBaseVolumeName
	if state != nil && strings.TrimSpace(state.Base.Name) != "" {
		baseName = state.Base.Name
	}
	listed, listErr := store.engine.ListSnapshots(baseName, 0, "")
	if listErr != nil && !isCowSemantic(listErr, cubecow.SemNotFound) {
		remountErr = errors.Join(remountErr, listErr)
	} else if listed != nil {
		for _, snap := range listed.Snapshots {
			id := parseS3MetadataSnapshotID(snap.Name)
			mountOne(id, "")
		}
	}
	entries, listCatErr := ListLocalSnapshotsFor(ctx, cow.BackendS3)
	if listCatErr != nil && !errors.Is(listCatErr, ErrSnapshotCatalogNotFound) {
		remountErr = errors.Join(remountErr, listCatErr)
	}
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		id := strings.TrimSpace(entry.SnapshotID)
		if name := strings.TrimSpace(entry.MetadataVol); name != "" {
			if parsed := parseS3MetadataSnapshotID(name); parsed != "" {
				id = parsed
			}
		}
		mountOne(id, strings.TrimSpace(entry.MetaDir))
	}
	return remountErr
}

func s3MetadataGuessMountPathLocked(snapshotID string) string {
	for _, kind := range []string{SnapshotKindPause, SnapshotKindNormal} {
		home := SnapshotHome(cow.BackendS3, kind, snapshotID)
		if home == "" {
			continue
		}
		if _, err := os.Stat(home); err == nil {
			return filepath.Join(home, SnapshotMetadataDir)
		}
	}
	return ""
}

func (m *S3Cow) deriveMetadataSnapshotLocked(ctx context.Context, snapshotID, sourceName string) (*cowVolume, error) {
	source := strings.TrimSpace(sourceName)
	if source == "" {
		source = S3MetadataBaseVolumeName
		if state, err := loadS3MetadataState(); err == nil && state != nil && strings.TrimSpace(state.Base.Name) != "" {
			source = state.Base.Name
		}
	}
	snapshotName := S3MetadataVolumeName(snapshotID)
	if IsS3MetadataBaseName(snapshotName) {
		return nil, fmt.Errorf("refusing to derive s3 metadata snapshot onto the node-local base")
	}
	devPath, err := m.createOrResolveSnapshotPathFromSource(ctx, source, snapshotName)
	if err != nil {
		return nil, fmt.Errorf("snapshot s3 metadata %s from %s: %w", snapshotName, source, err)
	}
	return newCowVolume(snapshotName, cowKindSnapshot, 0, devPath), nil
}

func mountS3MetadataLocked(snapshotID, devicePath, mountPath string) error {
	if err := pathutil.ValidateNoTraversal(mountPath); err != nil {
		return fmt.Errorf("invalid s3 metadata mount path: %w", err)
	}
	if prev := s3MetadataMounts[snapshotID]; prev != "" && filepath.Clean(prev) != filepath.Clean(mountPath) {
		if err := unmountS3MetadataPathIfMounted(prev); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(mountPath, 0o755); err != nil {
		return fmt.Errorf("mkdir s3 metadata mount %s: %w", mountPath, err)
	}
	if s3MetadataIsMounted(mountPath) {
		s3MetadataMounts[snapshotID] = filepath.Clean(mountPath)
		return nil
	}
	if err := mountS3MetadataDevice(devicePath, mountPath); err != nil {
		return err
	}
	s3MetadataMounts[snapshotID] = filepath.Clean(mountPath)
	return nil
}

func unmountS3MetadataPathIfMounted(mountPath string) error {
	mountPath = strings.TrimSpace(mountPath)
	if mountPath == "" {
		return nil
	}
	if !s3MetadataIsMounted(mountPath) {
		return nil
	}
	if err := unmountS3MetadataPath(mountPath); err != nil {
		return fmt.Errorf("umount s3 metadata %s: %w", mountPath, err)
	}
	for id, p := range s3MetadataMounts {
		if filepath.Clean(p) == filepath.Clean(mountPath) {
			delete(s3MetadataMounts, id)
		}
	}
	return nil
}

func persistDerivedLocked(snapshotID, volName, mountPath string) error {
	state, err := loadS3MetadataState()
	if err != nil {
		return err
	}
	if state.Derived == nil {
		state.Derived = map[string]s3MetadataDerivedRecord{}
	}
	state.Derived[snapshotID] = s3MetadataDerivedRecord{
		SnapshotID: snapshotID,
		VolName:    volName,
		MountPath:  filepath.Clean(mountPath),
	}
	return saveS3MetadataState(state)
}

func loadS3MetadataState() (*s3MetadataState, error) {
	state := &s3MetadataState{Derived: map[string]s3MetadataDerivedRecord{}}
	raw, err := readS3MetadataKV()
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(raw, state); err != nil {
		return nil, fmt.Errorf("decode s3 metadata state: %w", err)
	}
	if state.Derived == nil {
		state.Derived = map[string]s3MetadataDerivedRecord{}
	}
	return state, nil
}

func saveS3MetadataState(state *s3MetadataState) error {
	if state == nil {
		return nil
	}
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeS3MetadataKV(body)
}

func readS3MetadataKV() ([]byte, error) {
	if testS3MetadataKV != nil {
		return testS3MetadataKV.Get()
	}
	if localStorage == nil || localStorage.db == nil {
		return nil, nil
	}
	b, err := localStorage.db.Get(s3MetadataBucket, s3MetadataStateKey)
	if err != nil {
		if errors.Is(err, utils.ErrorKeyNotFound) || errors.Is(err, utils.ErrorBucketNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return b, nil
}

func writeS3MetadataKV(body []byte) error {
	if testS3MetadataKV != nil {
		return testS3MetadataKV.Set(body)
	}
	if localStorage == nil || localStorage.db == nil {
		return nil
	}
	return localStorage.db.Set(s3MetadataBucket, s3MetadataStateKey, body)
}

func formatS3MetadataBaseDeviceImpl(devicePath string) error {
	devicePath = strings.TrimSpace(devicePath)
	if devicePath == "" {
		return fmt.Errorf("s3 metadata base device path is required")
	}
	// Use 4096-byte blocks: s3lvol NVMe devices typically expose 4K
	// physical/logical sectors, and mkfs.ext4 -b 1024 fails with
	// "Invalid argument while setting blocksize; too small for device".
	cmds := [][]string{
		{"mkfs.ext4", "-F", "-O", "^has_journal", "-b", "4096", devicePath},
	}
	for _, cmd := range cmds {
		if _, stderr, err := utils.ExecV(cmd, cmdTimeout); err != nil {
			return fmt.Errorf("mkfs.ext4 s3 metadata base failed:%s", stderr)
		}
	}
	return nil
}

func mountS3MetadataDeviceImpl(devicePath, mountPath string) error {
	if _, stderr, err := utils.ExecV([]string{"mount", devicePath, mountPath}, cmdTimeout); err != nil {
		return fmt.Errorf("mount s3 metadata %s at %s failed:%s", devicePath, mountPath, stderr)
	}
	return nil
}

func unmountS3MetadataPathImpl(mountPath string) error {
	if _, stderr, err := utils.ExecV([]string{"umount", mountPath}, cmdTimeout); err != nil {
		if _, _, lazyErr := utils.ExecV([]string{"umount", "-l", mountPath}, cmdTimeout); lazyErr != nil {
			return fmt.Errorf("umount s3 metadata %s failed:%s", mountPath, stderr)
		}
	}
	return nil
}

func s3MetadataIsMountedImpl(mountPath string) bool {
	mountPath = filepath.Clean(strings.TrimSpace(mountPath))
	if mountPath == "" || mountPath == "." {
		return false
	}
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return s3MetadataIsMountedByDev(mountPath)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 {
			continue
		}
		if filepath.Clean(fields[4]) == mountPath {
			return true
		}
	}
	return s3MetadataIsMountedByDev(mountPath)
}

func s3MetadataIsMountedByDev(mountPath string) bool {
	var st, parent unix.Stat_t
	if err := unix.Stat(mountPath, &st); err != nil {
		return false
	}
	if err := unix.Stat(filepath.Dir(mountPath), &parent); err != nil {
		return false
	}
	return st.Dev != parent.Dev
}
