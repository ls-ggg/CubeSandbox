// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/tencentcloud/CubeSandbox/Cubelet/storage/cow"
)

type s3UploadEntry struct {
	state   string
	message string
	uuids   *cow.RemoteUUIDs
}

type cowSnapshotExporter interface {
	ExportSnapshot(name string) (string, error)
}

type cowVolumeImporter interface {
	ImportLvol(name, remoteUUID string) (string, error)
}

// Upload implements [cow.Uploader]. Calls cubecow_export_snapshot per
// snapshot disk (rootfs / memory / metadata). Failure returns an error and
// does not persist remote_uuids — Master treats that as upload failed.
// Success returns real export uuids and records local state as running;
// terminal ready comes later from cubecow export_status via UploadStatus.
func (m *S3Cow) Upload(ctx context.Context, snapshotID string) (*cow.RemoteUUIDs, error) {
	id := strings.TrimSpace(snapshotID)
	if id == "" {
		return nil, fmt.Errorf("snapshot_id is required")
	}
	uuids := &cow.RemoteUUIDs{}
	for _, ref := range activateObjectRefs(ctx, cow.BackendS3, id) {
		if IsS3MetadataBaseName(ref.Name) {
			continue
		}
		if ref.Role == "metadata" {
			info, infoErr := m.GetVolumeInfo(ctx, ref.Name)
			exists, existsErr := cowObjectPresent(info, infoErr)
			if existsErr != nil {
				m.setUpload(id, cow.RemoteStateFailed, existsErr.Error(), nil)
				return nil, fmt.Errorf("upload %s %s: %w", ref.Role, ref.Name, existsErr)
			}
			if !exists {
				continue
			}
		}
		uuid, err := m.uploadOne(ref.Name)
		if err != nil {
			m.setUpload(id, cow.RemoteStateFailed, err.Error(), nil)
			return nil, fmt.Errorf("upload %s %s: %w", ref.Role, ref.Name, err)
		}
		switch ref.Role {
		case "rootfs":
			uuids.Rootfs = uuid
		case "memory":
			uuids.Memory = uuid
		case "metadata":
			uuids.Metadata = uuid
		}
	}
	if uuids.Empty() {
		m.setUpload(id, cow.RemoteStateFailed, "empty remote_uuids", nil)
		return nil, fmt.Errorf("s3 export returned empty remote_uuids for %s", id)
	}
	if entry, err := GetLocalSnapshotFor(ctx, cow.BackendS3, id); err == nil && entry != nil {
		entry.RemoteUUIDs = uuids
		_ = WriteSnapshotCatalogFor(cow.BackendS3, entry)
	}
	// Export accepted; upload may still be in flight on the S3 backend.
	m.setUpload(id, cow.RemoteStateRunning, "export started", uuids)
	return uuids, nil
}

func (m *S3Cow) uploadOne(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("object name is required")
	}
	if IsS3MetadataBaseName(name) {
		return "", fmt.Errorf("refusing to export node-local s3 metadata base %s", name)
	}
	exp, ok := m.engine.(cowSnapshotExporter)
	if !ok || exp == nil {
		return "", fmt.Errorf("cubecow engine does not support export_snapshot")
	}
	// Export is snapshot-only; volumes must be sealed to a snapshot before Upload.
	uuid, err := exp.ExportSnapshot(name)
	if err != nil {
		return "", err
	}
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return "", fmt.Errorf("cubecow_export_snapshot returned empty uuid for %s", name)
	}
	return uuid, nil
}

// UploadStatus implements [cow.Uploader].
// Aggregates cubecow_get_volume_info export_status:
// NONE → pending, empty/INPROGRESS → running, DONE → ready.
func (m *S3Cow) UploadStatus(ctx context.Context, snapshotID string) (*cow.RemoteStatus, error) {
	id := strings.TrimSpace(snapshotID)
	if id == "" {
		return nil, fmt.Errorf("snapshot_id is required")
	}
	st := &cow.RemoteStatus{
		SnapshotID: id,
		State:      cow.RemoteStatePending,
	}

	m.uploadLock.Lock()
	entry, ok := m.uploadStates[id]
	m.uploadLock.Unlock()
	if ok {
		st.RemoteUUIDs = entry.uuids
		if entry.state == cow.RemoteStateFailed {
			st.State = cow.RemoteStateFailed
			st.Message = entry.message
			return st, nil
		}
	} else if cat, err := GetLocalSnapshotFor(ctx, cow.BackendS3, id); err == nil && cat != nil && !cat.RemoteUUIDs.Empty() {
		st.RemoteUUIDs = cat.RemoteUUIDs
	}

	refs := activateObjectRefs(ctx, cow.BackendS3, id)
	if len(refs) == 0 {
		st.State = cow.RemoteStateRunning
		st.Message = "waiting for local snapshot objects"
		return st, nil
	}

	var (
		sawAny       bool
		allDone      = true
		anyProgress  bool
		anyNone      bool
		anyDead      bool
		localVolumes []cow.VolumeRemoteInfo
		messages     []string
	)
	for _, ref := range refs {
		if IsS3MetadataBaseName(ref.Name) {
			continue
		}
		info, infoErr := m.GetVolumeInfo(ctx, ref.Name)
		exists, existsErr := cowObjectPresent(info, infoErr)
		if existsErr != nil {
			st.State = cow.RemoteStateFailed
			st.Message = existsErr.Error()
			return st, nil
		}
		vol := cow.VolumeRemoteInfo{Name: ref.Name, Role: ref.Role, Exists: exists}
		if info != nil {
			vol.DevicePath = strings.TrimSpace(info.DevicePath)
			if ref.Role == "rootfs" && info.Deletable != nil {
				v := *info.Deletable
				st.RootfsDeletable = &v
			}
		}
		localVolumes = append(localVolumes, vol)
		if !exists {
			if ref.Role == "metadata" {
				continue
			}
			allDone = false
			anyProgress = true
			continue
		}
		sawAny = true
		status := ""
		uuid := ""
		if info != nil {
			status = strings.ToUpper(strings.TrimSpace(info.ExportStatus))
			uuid = strings.TrimSpace(info.ExportUUID)
		}
		if uuid != "" {
			if st.RemoteUUIDs == nil {
				st.RemoteUUIDs = &cow.RemoteUUIDs{}
			}
			switch ref.Role {
			case "rootfs":
				if st.RemoteUUIDs.Rootfs == "" {
					st.RemoteUUIDs.Rootfs = uuid
				}
			case "memory":
				if st.RemoteUUIDs.Memory == "" {
					st.RemoteUUIDs.Memory = uuid
				}
			case "metadata":
				if st.RemoteUUIDs.Metadata == "" {
					st.RemoteUUIDs.Metadata = uuid
				}
			}
		}
		switch {
		case status == cow.ExportStatusDone:
			// ok
		case status == "" && uuid != "":
			// A recorded uuid that reports no status at all is a dead
			// export: s3lvol hands the uuid back before the transfer can
			// fail, then records nothing under it, so every later lookup
			// says no such export and cubecow leaves the status empty.
			// Waiting cannot fix it — only exporting again can — so this
			// is a terminal failure rather than an upload still in flight.
			allDone = false
			anyDead = true
			messages = append(messages, fmt.Sprintf("%s export %s no longer exists", ref.Name, uuid))
		case status == cow.ExportStatusInProgress, status == "":
			// Empty without a uuid means cubecow has not reported yet.
			allDone = false
			anyProgress = true
		case status == cow.ExportStatusNone:
			allDone = false
			anyNone = true
		default:
			allDone = false
			anyProgress = true
			messages = append(messages, fmt.Sprintf("%s=%s", ref.Name, status))
		}
	}
	st.LocalVolumes = localVolumes

	if !sawAny {
		st.State = cow.RemoteStateRunning
		st.Message = "waiting for snapshot volumes"
		return st, nil
	}

	switch {
	case anyDead:
		// Terminal: report it now so Master stops polling a package that
		// can never become usable and surfaces it as failed instead of
		// leaving it inprogress forever.
		st.State = cow.RemoteStateFailed
		st.Message = strings.Join(messages, "; ")
	case allDone:
		st.State = cow.RemoteStateReady
		st.Message = "export_status DONE"
	case anyProgress:
		st.State = cow.RemoteStateRunning
		if len(messages) > 0 {
			st.Message = strings.Join(messages, "; ")
		} else {
			st.Message = "export_status INPROGRESS"
		}
	case anyNone:
		st.State = cow.RemoteStatePending
		st.Message = "export_status NONE"
	default:
		st.State = cow.RemoteStateRunning
		st.Message = "export in progress"
	}
	return st, nil
}

func (m *S3Cow) setUpload(id, state, message string, uuids *cow.RemoteUUIDs) {
	m.uploadLock.Lock()
	defer m.uploadLock.Unlock()
	if m.uploadStates == nil {
		m.uploadStates = make(map[string]s3UploadEntry)
	}
	m.uploadStates[id] = s3UploadEntry{state: state, message: message, uuids: uuids}
}

var _ cow.Uploader = (*S3Cow)(nil)
