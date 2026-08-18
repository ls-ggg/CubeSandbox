// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package storage

import (
	"context"
	"fmt"
	"sync"

	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/cubecow"
	"github.com/tencentcloud/CubeSandbox/Cubelet/storage/cow"
	"github.com/tencentcloud/CubeSandbox/Cubelet/storage/cow/s3"
)

// S3Cow is a mock S3-backed CoW Store. It is a copy of the XFS/reflink path
// (same cubecow engine semantics) so cross-node S3 control-plane work can land
// before a real remote backend exists. The original [XfsCow] implementation is
// intentionally left untouched; do not merge mock-S3 logic back into it.
//
// create/activate/delete/list currently behave like XFS via the shared cubecow
// engine. Upload/Fetch/Activate map to cubecow_export_snapshot /
// cubecow_import_lvol / cubecow_activate_volume (upload is mocked while the
// linked cubecow is still reflink).
type S3Cow struct {
	engine cowEngine

	uploadLock   sync.Mutex
	uploadStates map[string]s3UploadEntry // snapshotID → remote uuids / state
}

func newS3CowVolumeManager(engine *cubecow.Engine) *S3Cow {
	return &S3Cow{
		engine:       engine,
		uploadStates: make(map[string]s3UploadEntry),
	}
}

// Name implements [cow.Store].
func (m *S3Cow) Name() string {
	return s3.Name
}

var _ cow.Store = (*S3Cow)(nil)

func (m *S3Cow) CreateDefaultMediumVolume(ctx context.Context, sandboxID, volumeName string, sizeBytes uint64) (*cowVolume, error) {
	name := fmt.Sprintf("sb-%s-%s", sandboxID, volumeName)
	return m.createInitializedVolume(ctx, name, sizeBytes)
}

func (m *S3Cow) createInitializedVolume(ctx context.Context, name string, sizeBytes uint64) (*cowVolume, error) {
	devPath, created, err := m.createOrResolveVolumePath(ctx, name, sizeBytes)
	if err != nil {
		return nil, err
	}
	if created {
		if err := m.initializeNewDefaultMediumVolume(ctx, name, devPath); err != nil {
			return nil, err
		}
	}
	return newCowVolume(name, cowKindVolume, 0, devPath), nil
}

func (m *S3Cow) createOrResolveVolumePath(ctx context.Context, name string, sizeBytes uint64) (string, bool, error) {
	devPath, err := m.engine.CreateVolume(name, sizeBytes)
	if err != nil {
		if !isCowSemantic(err, cubecow.SemAlreadyExists) {
			return "", false, err
		}
		devPath, err = m.ResolveDevPath(ctx, name, cowKindVolume)
		if err != nil {
			return "", false, err
		}
		return devPath, false, nil
	}
	return devPath, true, nil
}

func (m *S3Cow) initializeNewDefaultMediumVolume(ctx context.Context, name, devPath string) error {
	if err := initDefaultMediumDevice(devPath); err != nil {
		if cleanupErr := m.DeleteByKind(ctx, name, cowKindVolume); cleanupErr != nil {
			return fmt.Errorf("initialize cubecow default medium %s at %s: %w (cleanup failed: %v)", name, devPath, err, cleanupErr)
		}
		return fmt.Errorf("initialize cubecow default medium %s at %s: %w", name, devPath, err)
	}
	return nil
}

func (m *S3Cow) CreateSandboxRootfsFromTemplate(ctx context.Context, sandboxID, templateID string, gen uint32, desiredSizeBytes uint64) (*cowVolume, error) {
	sourceName := cowTemplateRootfsName(templateID)
	return m.RollbackDeriveNewGen(ctx, sandboxID, sourceName, gen, desiredSizeBytes)
}

func (m *S3Cow) RollbackDeriveNewGen(ctx context.Context, sandboxID, snapshotRootfsVol string, gen uint32, desiredSizeBytes uint64) (*cowVolume, error) {
	if snapshotRootfsVol == "" {
		return nil, fmt.Errorf("snapshot rootfs volume is required")
	}
	snapshotName := fmt.Sprintf("sb-%s-rootfs-gen%d", sandboxID, gen)
	devPath, err := m.createOrResolveSnapshotPathFromSource(ctx, snapshotRootfsVol, snapshotName)
	if err != nil {
		return nil, err
	}
	resized, err := m.resizeSnapshotIfTooSmall(snapshotName, desiredSizeBytes)
	if err != nil {
		return nil, err
	}
	if resized {
		devPath, err = m.ResolveDevPath(ctx, snapshotName, cowKindSnapshot)
		if err != nil {
			return nil, err
		}
	}
	return newCowVolume(snapshotName, cowKindSnapshot, gen, devPath), nil
}

func (m *S3Cow) resizeSnapshotIfTooSmall(snapshotName string, desiredSizeBytes uint64) (bool, error) {
	if desiredSizeBytes == 0 {
		return false, nil
	}
	info, err := m.engine.GetVolumeInfo(snapshotName)
	if err != nil {
		return false, err
	}
	if info == nil || info.SizeBytes >= desiredSizeBytes {
		return false, nil
	}
	if _, _, err := m.engine.ResizeVolume(snapshotName, desiredSizeBytes); err != nil {
		return false, err
	}
	return true, nil
}

func (m *S3Cow) CreateTemplateBuildRootfs(ctx context.Context, templateID string, sizeBytes uint64) (*cowVolume, error) {
	return m.createInitializedTemplateVolume(ctx, cowTemplateBuildRootfsName(templateID), sizeBytes)
}

func (m *S3Cow) CommitTemplateRootfs(ctx context.Context, sourceName, templateID string) (*cowVolume, error) {
	snapshotName := cowTemplateRootfsName(templateID)
	devPath, err := m.createTemplateSnapshotPath(sourceName, snapshotName)
	if err != nil {
		return nil, err
	}
	return newCowVolume(snapshotName, cowKindSnapshot, 0, devPath), nil
}

func (m *S3Cow) CreateMemoryVolume(ctx context.Context, templateID string, sizeBytes uint64) (*cowVolume, error) {
	name := cowTemplateMemoryName(templateID)
	devPath, err := m.createTemplateVolumePath(name, sizeBytes)
	if err != nil {
		return nil, err
	}
	if err := m.ensureVolumeSizeAtLeast(ctx, name, sizeBytes); err != nil {
		if cleanupErr := m.DeleteByKind(ctx, name, cowKindVolume); cleanupErr != nil {
			return nil, fmt.Errorf("%w (cleanup failed: %v)", err, cleanupErr)
		}
		return nil, err
	}
	resolvedPath, err := m.ResolveDevPath(ctx, name, cowKindVolume)
	if err != nil {
		return nil, err
	}
	if resolvedPath != "" {
		devPath = resolvedPath
	}
	return newCowVolume(name, cowKindVolume, 0, devPath), nil
}

// CommitTemplateMemory clones an existing memory object (sourceName) into the
// canonical template memory name for templateID via cubecow's reflink-backed
// CreateSnapshot. Unlike CreateMemoryVolume which produces an empty volume,
// this preserves the source memory bytes so the hypervisor can perform an
// incremental (pagemap_anon) snapshot that only overwrites CoW anonymous
// pages while keeping the rest of the base memory intact.
//
// activate=true is passed through so callers immediately receive a usable
// device path. With the reflink backend, activation is effectively a no-op
// (snapshots are addressable via their filesystem path).
func (m *S3Cow) CommitTemplateMemory(ctx context.Context, sourceName, templateID string, sizeBytes uint64) (*cowVolume, error) {
	snapshotName := cowTemplateMemoryName(templateID)
	devPath, err := m.engine.CreateSnapshot(sourceName, snapshotName, true)
	if err != nil {
		if isCowSemantic(err, cubecow.SemAlreadyExists) {
			return nil, fmt.Errorf("%w: name=%s kind=%s", ErrCowObjectAlreadyExists, snapshotName, cowKindSnapshot)
		}
		return nil, err
	}
	if sizeBytes > 0 {
		info, infoErr := m.engine.GetVolumeInfo(snapshotName)
		if infoErr != nil {
			if cleanupErr := m.DeleteByKind(ctx, snapshotName, cowKindSnapshot); cleanupErr != nil {
				return nil, fmt.Errorf("%w (cleanup failed: %v)", infoErr, cleanupErr)
			}
			return nil, infoErr
		}
		actual := uint64(0)
		if info != nil {
			actual = info.SizeBytes
		}
		if actual < sizeBytes {
			sizeErr := fmt.Errorf("cloned memory snapshot %s size %d is smaller than requested %d", snapshotName, actual, sizeBytes)
			if cleanupErr := m.DeleteByKind(ctx, snapshotName, cowKindSnapshot); cleanupErr != nil {
				return nil, fmt.Errorf("%w (cleanup failed: %v)", sizeErr, cleanupErr)
			}
			return nil, sizeErr
		}
	}
	resolvedPath, err := m.ResolveDevPath(ctx, snapshotName, cowKindSnapshot)
	if err != nil {
		return nil, err
	}
	if resolvedPath != "" {
		devPath = resolvedPath
	}
	return newCowVolume(snapshotName, cowKindSnapshot, 0, devPath), nil
}

func (m *S3Cow) createInitializedTemplateVolume(ctx context.Context, name string, sizeBytes uint64) (*cowVolume, error) {
	devPath, err := m.createTemplateVolumePath(name, sizeBytes)
	if err != nil {
		return nil, err
	}
	if err := m.initializeNewDefaultMediumVolume(ctx, name, devPath); err != nil {
		return nil, err
	}
	return newCowVolume(name, cowKindVolume, 0, devPath), nil
}

func (m *S3Cow) createTemplateVolumePath(name string, sizeBytes uint64) (string, error) {
	devPath, err := m.engine.CreateVolume(name, sizeBytes)
	if err != nil {
		if isCowSemantic(err, cubecow.SemAlreadyExists) {
			return "", fmt.Errorf("%w: name=%s kind=%s", ErrCowObjectAlreadyExists, name, cowKindVolume)
		}
		return "", err
	}
	return devPath, nil
}

func (m *S3Cow) createTemplateSnapshotPath(sourceName, snapshotName string) (string, error) {
	devPath, err := m.engine.CreateSnapshot(sourceName, snapshotName, false)
	if err != nil {
		if isCowSemantic(err, cubecow.SemAlreadyExists) {
			return "", fmt.Errorf("%w: name=%s kind=%s", ErrCowObjectAlreadyExists, snapshotName, cowKindSnapshot)
		}
		return "", err
	}
	return devPath, nil
}

func (m *S3Cow) ensureVolumeSizeAtLeast(ctx context.Context, name string, requestedSizeBytes uint64) error {
	if requestedSizeBytes == 0 {
		return nil
	}
	actualSizeBytes, err := m.GetSizeBytes(ctx, name)
	if err != nil {
		return err
	}
	if actualSizeBytes >= requestedSizeBytes {
		return nil
	}
	if _, _, err := m.engine.ResizeVolume(name, requestedSizeBytes); err != nil {
		return fmt.Errorf("resize cubecow volume %s from %d to %d bytes: %w", name, actualSizeBytes, requestedSizeBytes, err)
	}
	actualSizeBytes, err = m.GetSizeBytes(ctx, name)
	if err != nil {
		return err
	}
	if actualSizeBytes < requestedSizeBytes {
		return fmt.Errorf("cubecow volume %s size %d is smaller than requested %d", name, actualSizeBytes, requestedSizeBytes)
	}
	return nil
}

func (m *S3Cow) createOrResolveSnapshotPathFromSource(ctx context.Context, sourceName, snapshotName string) (string, error) {
	devPath, err := m.engine.CreateSnapshot(sourceName, snapshotName, true)
	if err != nil {
		if !isCowSemantic(err, cubecow.SemAlreadyExists) {
			return "", err
		}
		if err := m.ensureSnapshotOrigin(sourceName, snapshotName); err != nil {
			return "", err
		}
		devPath, err = m.ResolveDevPath(ctx, snapshotName, cowKindSnapshot)
		if err != nil {
			return "", err
		}
	}
	return devPath, nil
}

func (m *S3Cow) ensureSnapshotOrigin(sourceName, snapshotName string) error {
	result, err := m.engine.ListSnapshots(sourceName, 0, "")
	if err != nil {
		return fmt.Errorf("verify existing snapshot %s origin from %s: %w", snapshotName, sourceName, err)
	}
	if result == nil {
		return fmt.Errorf("%w: name=%s kind=%s origin=%s", ErrCowObjectAlreadyExists, snapshotName, cowKindSnapshot, sourceName)
	}
	for _, snapshot := range result.Snapshots {
		if snapshot.Name != snapshotName {
			continue
		}
		if snapshot.OriginVolume != "" && snapshot.OriginVolume != sourceName {
			return fmt.Errorf("existing snapshot %s origin %s does not match expected %s", snapshotName, snapshot.OriginVolume, sourceName)
		}
		return nil
	}
	return fmt.Errorf("%w: name=%s kind=%s origin=%s", ErrCowObjectAlreadyExists, snapshotName, cowKindSnapshot, sourceName)
}

func (m *S3Cow) DeleteByKind(ctx context.Context, name, kind string) error {
	_ = ctx
	deleteFn, err := m.deleteFunc(kind)
	if err != nil {
		return err
	}
	if err = deleteFn(name); err == nil || isCowSemantic(err, cubecow.SemNotFound) {
		return nil
	}
	// FIX-3: the recorded kind may not match the object's real cubecow type
	// (e.g. an incremental memory snapshot recorded/derived as a volume).
	// cubecow returns SemInvalidArgument ("is a snapshot; use delete_snapshot")
	// in that case. Retry with the opposite delete function so cleanup stays
	// kind-agnostic and idempotent rather than leaking the object.
	if isCowSemantic(err, cubecow.SemInvalidArgument) {
		if otherFn := m.oppositeDeleteFunc(kind); otherFn != nil {
			if retryErr := otherFn(name); retryErr == nil || isCowSemantic(retryErr, cubecow.SemNotFound) {
				return nil
			}
		}
		return err
	}
	if isCowSemantic(err, cubecow.SemIoError) {
		if retryErr := deleteFn(name); retryErr == nil || isCowSemantic(retryErr, cubecow.SemNotFound) {
			return nil
		} else {
			return retryErr
		}
	}
	return err
}

func (m *S3Cow) DeactivateByKind(ctx context.Context, name, kind string) error {
	_ = ctx
	if _, err := m.deleteFunc(kind); err != nil {
		return err
	}
	return m.engine.DeactivateVolume(name)
}

func (m *S3Cow) ResolveDevPath(ctx context.Context, name, kind string) (string, error) {
	_ = ctx
	if _, err := m.deleteFunc(kind); err != nil {
		return "", err
	}
	info, err := m.engine.GetVolumeInfo(name)
	if err != nil {
		return "", err
	}
	if info == nil {
		return "", fmt.Errorf("cubecow object %q has empty info", name)
	}
	if info.DevicePath == "" {
		devPath, err := m.engine.ActivateVolume(name)
		if err != nil {
			return "", err
		}
		if devPath == "" {
			return "", fmt.Errorf("cubecow object %q has empty device path after activate", name)
		}
		return devPath, nil
	}
	return info.DevicePath, nil
}

func (m *S3Cow) GetSizeBytes(ctx context.Context, name string) (uint64, error) {
	_ = ctx
	info, err := m.engine.GetVolumeInfo(name)
	if err != nil {
		return 0, err
	}
	if info == nil {
		return 0, fmt.Errorf("cubecow object %q has empty info", name)
	}
	return info.SizeBytes, nil
}

func (m *S3Cow) GetVolumeInfo(ctx context.Context, name string) (*cubecow.Volume, error) {
	_ = ctx
	return m.engine.GetVolumeInfo(name)
}

func (m *S3Cow) GetMetrics(ctx context.Context) (map[string]uint64, error) {
	_ = ctx
	return m.engine.GetMetrics()
}

func (m *S3Cow) deleteFunc(kind string) (func(string) error, error) {
	switch kind {
	case cowKindVolume:
		return m.engine.DeleteVolume, nil
	case cowKindSnapshot:
		return m.engine.DeleteSnapshot, nil
	default:
		return nil, fmt.Errorf("unsupported cubecow kind %q", kind)
	}
}

// oppositeDeleteFunc returns the delete function for the other cubecow kind, so
// DeleteByKind can recover when the recorded kind does not match the object's
// real type. Returns nil for an unrecognized kind.
func (m *S3Cow) oppositeDeleteFunc(kind string) func(string) error {
	switch kind {
	case cowKindVolume:
		return m.engine.DeleteSnapshot
	case cowKindSnapshot:
		return m.engine.DeleteVolume
	default:
		return nil
	}
}
