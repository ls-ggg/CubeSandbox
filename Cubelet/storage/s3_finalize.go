// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/cubecow"
	"github.com/tencentcloud/CubeSandbox/Cubelet/storage/cow"
)

// FinalizeS3PackageSnapshots is a Pause／Commit／AppSnapshot packaging step:
// seal RW memory／metadata work volumes into RO snapshots, rewrite the local
// catalog, umount the package metadata mount, and delete the work volumes
// (snapshots remain; volumes follow the sandbox／build lifecycle).
// Rootfs is already a snapshot from CommitTemplateRootfs. XFS is a no-op.
func FinalizeS3PackageSnapshots(ctx context.Context, backend, snapshotID string) error {
	normalized, err := cow.NormalizeBackend(backend)
	if err != nil || normalized != cow.BackendS3 {
		return nil
	}
	id := strings.TrimSpace(snapshotID)
	if id == "" {
		return fmt.Errorf("snapshot_id is required")
	}
	store, err := requireCowStoreFor(cow.BackendS3)
	if err != nil {
		return err
	}
	s3store, ok := store.(*S3Cow)
	if !ok || s3store == nil {
		return fmt.Errorf("s3 cow store is not *S3Cow")
	}
	entry, err := GetLocalSnapshotFor(ctx, cow.BackendS3, id)
	if err != nil || entry == nil {
		return fmt.Errorf("s3 package catalog %s: %w", id, err)
	}

	memoryWork := strings.TrimSpace(entry.MemoryVol)
	metaWork := strings.TrimSpace(entry.MetadataVol)
	if metaWork == "" {
		metaWork = S3MetadataVolumeName(id)
	}

	changed := false
	if memoryWork != "" {
		snap, sealed, sealErr := s3store.sealVolumeToSnapshot(ctx, memoryWork)
		if sealErr != nil {
			return fmt.Errorf("seal memory %s: %w", memoryWork, sealErr)
		}
		if sealed {
			entry.MemoryVol = snap
			entry.MemoryKind = cowKindSnapshot
			changed = true
		} else if strings.TrimSpace(entry.MemoryKind) != cowKindSnapshot {
			entry.MemoryKind = cowKindSnapshot
			changed = true
		}
	}
	if metaWork != "" && !IsS3MetadataBaseName(metaWork) {
		desiredSnap := S3MetadataSnapshotName(id)
		if strings.HasSuffix(metaWork, "-snap") {
			desiredSnap = metaWork
		}
		info, infoErr := s3store.GetVolumeInfo(ctx, metaWork)
		exists, existsErr := cowObjectPresent(info, infoErr)
		if existsErr != nil {
			return existsErr
		}
		if exists {
			snap, sealed, sealErr := s3store.sealVolumeToSnapshotAs(ctx, metaWork, desiredSnap)
			if sealErr != nil {
				return fmt.Errorf("seal metadata %s: %w", metaWork, sealErr)
			}
			if sealed || strings.TrimSpace(entry.MetadataVol) != snap || strings.TrimSpace(entry.MetadataKind) != cowKindSnapshot {
				entry.MetadataVol = snap
				entry.MetadataKind = cowKindSnapshot
				changed = true
			}
		} else if snapInfo, snapErr := s3store.GetVolumeInfo(ctx, desiredSnap); snapErr == nil && snapInfo != nil {
			if strings.TrimSpace(entry.MetadataVol) != desiredSnap || strings.TrimSpace(entry.MetadataKind) != cowKindSnapshot {
				entry.MetadataVol = desiredSnap
				entry.MetadataKind = cowKindSnapshot
				changed = true
			}
		}
	}
	if changed {
		if err := WriteSnapshotCatalogFor(cow.BackendS3, entry); err != nil {
			return err
		}
	}

	// Drop the package mount and delete RW work volumes; keep sealed snaps.
	metaDir := strings.TrimSpace(entry.MetaDir)
	if metaDir == "" {
		if home := strings.TrimSpace(entry.SnapshotPath); home != "" {
			metaDir = filepath.Join(home, SnapshotMetadataDir)
		} else {
			metaDir = SnapshotMetaDir(cow.BackendS3, SnapshotKindNormal, id)
		}
	}
	_ = UnmountS3Metadata(metaDir)
	if pauseHome := SnapshotHome(cow.BackendS3, SnapshotKindPause, id); pauseHome != "" {
		_ = UnmountS3Metadata(filepath.Join(pauseHome, SnapshotMetadataDir))
	}

	var cleanupErr error
	memorySnap := strings.TrimSpace(entry.MemoryVol)
	if memoryWork != "" && memoryWork != memorySnap && !strings.HasSuffix(memoryWork, "-snap") {
		if err := s3store.DeleteByKind(ctx, memoryWork, cowKindVolume); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("delete memory work %s: %w", memoryWork, err))
		}
	}
	metaSnap := strings.TrimSpace(entry.MetadataVol)
	if metaWork != "" && metaWork != metaSnap && !IsS3MetadataBaseName(metaWork) && !strings.HasSuffix(metaWork, "-snap") {
		if err := s3store.DeleteByKind(ctx, metaWork, cowKindVolume); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("delete metadata work %s: %w", metaWork, err))
		}
	}
	return cleanupErr
}

// sealVolumeToSnapshot creates name+"-snap" from a RW volume. If name is
// already a snapshot, returns name unchanged.
func (m *S3Cow) sealVolumeToSnapshot(ctx context.Context, volumeName string) (snapName string, sealed bool, err error) {
	return m.sealVolumeToSnapshotAs(ctx, volumeName, strings.TrimSpace(volumeName)+"-snap")
}

func (m *S3Cow) sealVolumeToSnapshotAs(ctx context.Context, volumeName, snapName string) (string, bool, error) {
	_ = ctx
	volumeName = strings.TrimSpace(volumeName)
	snapName = strings.TrimSpace(snapName)
	if volumeName == "" || snapName == "" {
		return "", false, fmt.Errorf("volume and snapshot names are required")
	}
	if IsS3MetadataBaseName(volumeName) || IsS3MetadataBaseName(snapName) {
		return "", false, fmt.Errorf("refusing to seal node-local s3 metadata base")
	}
	info, infoErr := m.GetVolumeInfo(ctx, snapName)
	exists, err := cowObjectPresent(info, infoErr)
	if err != nil {
		return "", false, err
	}
	if exists {
		return snapName, false, nil
	}
	// If the catalog already points at a snapshot under volumeName, keep it.
	if volumeName == snapName {
		return snapName, false, nil
	}
	srcInfo, srcErr := m.GetVolumeInfo(ctx, volumeName)
	srcExists, srcPresentErr := cowObjectPresent(srcInfo, srcErr)
	if srcPresentErr != nil {
		return "", false, srcPresentErr
	}
	if !srcExists {
		return "", false, fmt.Errorf("%w: %s", ErrCowObjectMissing, volumeName)
	}
	if _, err := m.engine.CreateSnapshotFromVolume(volumeName, snapName, false); err != nil {
		if !isCowSemantic(err, cubecow.SemAlreadyExists) {
			// Source may already be a snapshot — treat as sealed under volumeName.
			if isCowSemantic(err, cubecow.SemInvalidArgument) {
				return volumeName, false, nil
			}
			return "", false, err
		}
	}
	return snapName, true, nil
}
