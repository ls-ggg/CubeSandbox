// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/cubecow"
	"github.com/tencentcloud/CubeSandbox/Cubelet/storage/cow"
)

// requireCowStore returns the default (XFS) [cow.Store].
func requireCowStore() (cow.Store, error) {
	return requireCowStoreFor(cow.BackendXFS)
}

// requireCowStoreFor returns the [cow.Store] for the given backend type
// (request `type`: xfs｜s3). Empty / unknown aliases are normalized via
// [cow.NormalizeBackend].
func requireCowStoreFor(backend string) (cow.Store, error) {
	if localStorage == nil {
		return nil, fmt.Errorf("storage is not initialized")
	}
	if !localStorage.useCowStorage() {
		return nil, fmt.Errorf("storage backend is not cubecow")
	}
	if err := localStorage.ensureCowManager(); err != nil {
		return nil, err
	}
	store, err := StoreFor(backend)
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, fmt.Errorf("cow store is not initialized")
	}
	return store, nil
}

// SyncSnapshot triggers a mock/real remote sync for snapshotID on the S3 Store.
// backend must normalize to s3; XFS returns an error (no remote sync).
func SyncSnapshot(ctx context.Context, backend, snapshotID string) error {
	syncer, err := requireCowSyncer(backend)
	if err != nil {
		return err
	}
	return syncer.Sync(ctx, snapshotID)
}

// SnapshotSyncStatus returns the sync state for snapshotID on the S3 Store.
func SnapshotSyncStatus(ctx context.Context, backend, snapshotID string) (*cow.SyncStatus, error) {
	syncer, err := requireCowSyncer(backend)
	if err != nil {
		return nil, err
	}
	return syncer.SyncStatus(ctx, snapshotID)
}

func requireCowSyncer(backend string) (cow.Syncer, error) {
	normalized, err := cow.NormalizeBackend(backend)
	if err != nil {
		return nil, err
	}
	if normalized != cow.BackendS3 {
		return nil, fmt.Errorf("sync is only supported on %q backend (got %q)", cow.BackendS3, normalized)
	}
	store, err := requireCowStoreFor(cow.BackendS3)
	if err != nil {
		return nil, err
	}
	syncer, ok := store.(cow.Syncer)
	if !ok || syncer == nil {
		return nil, fmt.Errorf("s3 cow store does not implement sync")
	}
	return syncer, nil
}

// StoreFor returns the live XFS or S3 [cow.Store] for backend (request `type`).
// Both Stores are initialized together when cubecow storage is enabled.
func StoreFor(backend string) (cow.Store, error) {
	normalized, err := cow.NormalizeBackend(backend)
	if err != nil {
		return nil, err
	}
	if localStorage == nil {
		return nil, fmt.Errorf("storage is not initialized")
	}
	if err := localStorage.ensureCowManager(); err != nil {
		return nil, err
	}
	switch normalized {
	case cow.BackendS3:
		if localStorage.s3CowManager == nil {
			return nil, fmt.Errorf("s3 cow store is not initialized")
		}
		return localStorage.s3CowManager, nil
	default:
		if localStorage.cowManager == nil {
			return nil, fmt.Errorf("xfs cow store is not initialized")
		}
		return localStorage.cowManager, nil
	}
}

// GetSandboxRootfs resolves the live sandbox rootfs CoW object.
func GetSandboxRootfs(ctx context.Context, sandboxID, preferredVolumeName string) (*CowSnapshotObject, error) {
	if localStorage == nil {
		return nil, fmt.Errorf("storage is not initialized")
	}
	if !localStorage.useCowStorage() {
		return nil, fmt.Errorf("storage backend is not cubecow")
	}
	info, err := localStorage.readBackendFileInfo(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	rootfs, err := selectSnapshotRootfs(info, preferredVolumeName)
	if err != nil {
		return nil, err
	}
	return backendFileInfoToSnapshotObject(ctx, localStorage.cowManager, rootfs)
}

// CommitRootfs commits source rootfs into tpl-<id>-rootfs on the default (XFS) Store.
func CommitRootfs(ctx context.Context, source *CowSnapshotObject, id string) (*CowSnapshotObject, error) {
	return CommitRootfsFor(ctx, cow.BackendXFS, source, id)
}

// CommitRootfsFor is [CommitRootfs] on the Store selected by backend (request `type`).
func CommitRootfsFor(ctx context.Context, backend string, source *CowSnapshotObject, id string) (*CowSnapshotObject, error) {
	store, err := requireCowStoreFor(backend)
	if err != nil {
		return nil, err
	}
	if source == nil || source.Name == "" {
		return nil, fmt.Errorf("source rootfs is required")
	}
	volume, err := store.CommitTemplateRootfs(ctx, source.Name, id)
	if err != nil {
		return nil, err
	}
	return cubecowVolumeToSnapshotObjectWithoutActivation(ctx, store, volume)
}

// CommitRootfsFromBuild commits tpl-<id>-build-rootfs into tpl-<id>-rootfs.
func CommitRootfsFromBuild(ctx context.Context, id string) (*CowSnapshotObject, error) {
	return CommitRootfsFromBuildFor(ctx, cow.BackendXFS, id)
}

// CommitRootfsFromBuildFor is [CommitRootfsFromBuild] on the Store for backend.
func CommitRootfsFromBuildFor(ctx context.Context, backend, id string) (*CowSnapshotObject, error) {
	store, err := requireCowStoreFor(backend)
	if err != nil {
		return nil, err
	}
	volume, err := store.CommitTemplateRootfs(ctx, cowTemplateBuildRootfsName(id), id)
	if err != nil {
		return nil, err
	}
	return cubecowVolumeToSnapshotObjectWithoutActivation(ctx, store, volume)
}

// CreateMemoryVolume creates empty tpl-<id>-memory via the default (XFS) Store.
func CreateMemoryVolume(ctx context.Context, id string, sizeBytes uint64) (*CowSnapshotObject, error) {
	return CreateMemoryVolumeFor(ctx, cow.BackendXFS, id, sizeBytes)
}

// CreateMemoryVolumeFor is [CreateMemoryVolume] on the Store for backend.
func CreateMemoryVolumeFor(ctx context.Context, backend, id string, sizeBytes uint64) (*CowSnapshotObject, error) {
	store, err := requireCowStoreFor(backend)
	if err != nil {
		return nil, err
	}
	volume, err := store.CreateMemoryVolume(ctx, id, sizeBytes)
	if err != nil {
		return nil, err
	}
	return cubecowVolumeToSnapshotObject(ctx, store, volume)
}

// CommitMemoryFromBase clones source memory into tpl-<id>-memory via the default Store.
func CommitMemoryFromBase(ctx context.Context, source *CowSnapshotObject, id string, sizeBytes uint64) (*CowSnapshotObject, error) {
	return CommitMemoryFromBaseFor(ctx, cow.BackendXFS, source, id, sizeBytes)
}

// CommitMemoryFromBaseFor is [CommitMemoryFromBase] on the Store for backend.
func CommitMemoryFromBaseFor(ctx context.Context, backend string, source *CowSnapshotObject, id string, sizeBytes uint64) (*CowSnapshotObject, error) {
	store, err := requireCowStoreFor(backend)
	if err != nil {
		return nil, err
	}
	if source == nil || strings.TrimSpace(source.Name) == "" {
		return nil, fmt.Errorf("source memory object is required")
	}
	volume, err := store.CommitTemplateMemory(ctx, source.Name, id, sizeBytes)
	if err != nil {
		return nil, err
	}
	return cubecowVolumeToSnapshotObject(ctx, store, volume)
}

// DeleteObject deletes a CoW volume/snapshot by name and kind on the default Store.
func DeleteObject(ctx context.Context, name, kind string) error {
	return DeleteObjectFor(ctx, cow.BackendXFS, name, kind)
}

// DeleteObjectFor is [DeleteObject] on the Store for backend.
func DeleteObjectFor(ctx context.Context, backend, name, kind string) error {
	store, err := requireCowStoreFor(backend)
	if err != nil {
		return err
	}
	return store.DeleteByKind(ctx, name, kind)
}

// DeactivateObject deactivates a CoW volume/snapshot on the default Store.
func DeactivateObject(ctx context.Context, name, kind string) error {
	return DeactivateObjectFor(ctx, cow.BackendXFS, name, kind)
}

// DeactivateObjectFor is [DeactivateObject] on the Store for backend.
func DeactivateObjectFor(ctx context.Context, backend, name, kind string) error {
	store, err := requireCowStoreFor(backend)
	if err != nil {
		return err
	}
	return store.DeactivateByKind(ctx, name, kind)
}

// ResolveObjectPath resolves a CoW object to a device/file path on the default Store.
func ResolveObjectPath(ctx context.Context, name, kind string) (string, error) {
	return ResolveObjectPathFor(ctx, cow.BackendXFS, name, kind)
}

// ResolveObjectPathFor is [ResolveObjectPath] on the Store for backend.
func ResolveObjectPathFor(ctx context.Context, backend, name, kind string) (string, error) {
	store, err := requireCowStoreFor(backend)
	if err != nil {
		return "", err
	}
	normalizedKind, err := normalizeCowKind(kind)
	if err != nil {
		return "", err
	}
	return store.ResolveDevPath(ctx, name, normalizedKind)
}

// CleanupObjects best-effort deletes the given CoW object refs.
func CleanupObjects(ctx context.Context, refs []CowObjectRef) error {
	store, err := requireCowStore()
	if err != nil {
		return err
	}
	var cleanupErr error
	for _, ref := range refs {
		if strings.TrimSpace(ref.Name) == "" {
			continue
		}
		kind, err := normalizeCowKindForRole(ref.Kind, ref.Role)
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("cleanup cubecow object %q: %w", ref.Name, err))
			continue
		}
		if err := store.DeleteByKind(ctx, ref.Name, kind); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("cleanup cubecow object %q: %w", ref.Name, err))
			continue
		}
	}
	return cleanupErr
}

// InspectObjects reports existence/device path for CoW object refs.
func InspectObjects(ctx context.Context, refs []CowObjectRef) ([]CowObjectStatus, error) {
	store, err := requireCowStore()
	if err != nil {
		return nil, err
	}
	statuses := make([]CowObjectStatus, 0, len(refs))
	for _, ref := range refs {
		status := CowObjectStatus{
			Name: ref.Name,
			Kind: ref.Kind,
			Role: ref.Role,
		}
		if strings.TrimSpace(ref.Name) == "" {
			statuses = append(statuses, status)
			continue
		}
		kind, err := normalizeCowKind(ref.Kind)
		if err != nil {
			return nil, fmt.Errorf("inspect cubecow object %q: %w", ref.Name, err)
		}
		status.Kind = kind
		info, err := store.GetVolumeInfo(ctx, ref.Name)
		if err != nil {
			if isCowSemantic(err, cubecow.SemNotFound) {
				statuses = append(statuses, status)
				continue
			}
			return nil, fmt.Errorf("inspect cubecow object %q: %w", ref.Name, err)
		}
		if info == nil {
			statuses = append(statuses, status)
			continue
		}
		status.Exists = true
		status.DevicePath = info.DevicePath
		status.SizeBytes = info.SizeBytes
		statuses = append(statuses, status)
	}
	return statuses, nil
}

// ObjectMetrics returns backend metrics from the active [cow.Store].
func ObjectMetrics(ctx context.Context) (map[string]uint64, error) {
	store, err := requireCowStore()
	if err != nil {
		return nil, err
	}
	metrics, err := store.GetMetrics(ctx)
	if err != nil {
		return nil, err
	}
	for _, key := range cowMetricKeys {
		if _, ok := metrics[key]; !ok {
			return nil, fmt.Errorf("cubecow metric %q is missing", key)
		}
	}
	return metrics, nil
}

// ResolveRollbackRefs resolves rootfs/memory objects for rollback.
func ResolveRollbackRefs(ctx context.Context, rootfsVol, memoryVol, memoryKind string) (*CowRollbackSnapshotRefs, error) {
	store, err := requireCowStore()
	if err != nil {
		return nil, err
	}
	rootfs, err := resolveCowObject(ctx, store, rootfsVol, cowKindSnapshot, 0)
	if err != nil {
		return nil, err
	}
	normalizedMemoryKind, err := resolveRollbackMemoryKind(memoryKind)
	if err != nil {
		return nil, err
	}
	memory, err := resolveCowObject(ctx, store, memoryVol, normalizedMemoryKind, 0)
	if err != nil {
		return nil, err
	}
	return &CowRollbackSnapshotRefs{Rootfs: rootfs, Memory: memory}, nil
}

// DeriveRollbackRootfs creates sb-<id>-rootfs-genN from a snapshot rootfs.
func DeriveRollbackRootfs(ctx context.Context, sandboxID, snapshotRootfsVol string, newGen uint32, desiredSizeBytes uint64) (*CowSnapshotObject, error) {
	store, err := requireCowStore()
	if err != nil {
		return nil, err
	}
	volume, err := store.RollbackDeriveNewGen(ctx, sandboxID, snapshotRootfsVol, newGen, desiredSizeBytes)
	if err != nil {
		return nil, err
	}
	return cubecowVolumeToSnapshotObject(ctx, store, volume)
}

// PersistSandboxRootfs records the live sandbox rootfs after rollback.
func PersistSandboxRootfs(ctx context.Context, sandboxID string, rootfs *CowSnapshotObject) error {
	if localStorage == nil {
		return fmt.Errorf("storage is not initialized")
	}
	if rootfs == nil || rootfs.Name == "" {
		return fmt.Errorf("rollback rootfs is required")
	}
	info, err := localStorage.readBackendFileInfo(ctx, sandboxID)
	if err != nil {
		return err
	}
	current, err := selectSnapshotRootfs(info, rootfs.MountName)
	if err != nil {
		return err
	}
	current.VolumeName = rootfs.Name
	current.Kind = rootfs.Kind
	current.Gen = rootfs.Gen
	current.FilePath = rootfs.DevPath
	current.SizeLimit = int64(rootfs.SizeBytes)
	info.UpdateAt = time.Now()
	return localStorage.writeBackendFileInfo(ctx, sandboxID, info)
}
