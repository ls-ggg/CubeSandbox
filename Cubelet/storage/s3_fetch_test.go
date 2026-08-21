// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tencentcloud/CubeSandbox/Cubelet/storage/cow"
)

// A cross-node sandbox imports under its own names, so that destroy can
// delete what it created and the package keeps its objects for later creates.
func TestFetchAsImportsUnderCallerNames(t *testing.T) {
	engine := &fakeCowEngine{}
	store := &S3Cow{engine: engine}

	targets := []CowObjectRef{
		{Name: SandboxRootfsName("sb-1", 0), Kind: cowKindVolume, Role: "rootfs"},
		{Name: SandboxMemoryName("sb-1"), Kind: cowKindVolume, Role: "memory"},
	}
	uuids := &cow.RemoteUUIDs{Rootfs: "uuid-root", Memory: "uuid-mem"}
	require.NoError(t, store.FetchAs(context.Background(), targets, uuids, true))

	require.Equal(t, [][2]string{
		{"sb-sb-1-rootfs-gen0", "uuid-root"},
		{"sb-sb-1-memory", "uuid-mem"},
	}, engine.importedLvols)
	require.Equal(t, []string{"sb-sb-1-rootfs-gen0", "sb-sb-1-memory"}, engine.activatedVolumes)
}

// A role Master did not send has no export to import from; skip it rather
// than inventing a name. Rootfs-only packages rely on this for memory.
func TestFetchAsSkipsRolesWithoutUUID(t *testing.T) {
	engine := &fakeCowEngine{}
	store := &S3Cow{engine: engine}

	targets := []CowObjectRef{
		{Name: SandboxRootfsName("sb-2", 0), Kind: cowKindVolume, Role: "rootfs"},
		{Name: SandboxMemoryName("sb-2"), Kind: cowKindVolume, Role: "memory"},
	}
	uuids := &cow.RemoteUUIDs{Rootfs: "uuid-root"}
	require.NoError(t, store.FetchAs(context.Background(), targets, uuids, false))

	require.Equal(t, [][2]string{{"sb-sb-2-rootfs-gen0", "uuid-root"}}, engine.importedLvols)
	require.Empty(t, engine.activatedVolumes)
}

func TestFetchAsRefusesMetadataBase(t *testing.T) {
	engine := &fakeCowEngine{}
	store := &S3Cow{engine: engine}

	targets := []CowObjectRef{{Name: S3MetadataBaseVolumeName, Kind: cowKindVolume, Role: "metadata"}}
	err := store.FetchAs(context.Background(), targets, &cow.RemoteUUIDs{Metadata: "uuid-meta"}, false)
	require.Error(t, err)
	require.Empty(t, engine.importedLvols)
}
