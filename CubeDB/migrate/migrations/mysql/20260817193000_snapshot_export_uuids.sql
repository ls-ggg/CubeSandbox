-- Copyright (c) 2026 Tencent Inc.
-- SPDX-License-Identifier: Apache-2.0
--
-- Persist cubecow_export_snapshot uuids (rootfs/memory/metadata JSON)
-- on snapshot and pause-snapshot rows for cross-node cubecow_import_lvol.

-- +goose NO TRANSACTION
-- +goose Up

CALL cubemaster_acquire_migration_lock('cubemaster_migration_20260817193000_export_uuids', 60);

CALL cubemaster_assert_table_exists('t_cube_snapshot');
CALL cubemaster_assert_table_exists('t_cube_pause_snapshot');

CALL cubemaster_add_column_if_missing(
  't_cube_snapshot',
  'export_uuids',
  "mediumtext COMMENT 'JSON cubecow export_uuids: rootfs/memory/metadata'"
);

CALL cubemaster_add_column_if_missing(
  't_cube_pause_snapshot',
  'export_uuids',
  "mediumtext COMMENT 'JSON cubecow export_uuids: rootfs/memory/metadata'"
);

SELECT RELEASE_LOCK('cubemaster_migration_20260817193000_export_uuids');

-- +goose Down

CALL cubemaster_acquire_migration_lock('cubemaster_migration_20260817193000_export_uuids', 60);

CALL cubemaster_drop_column_if_exists('t_cube_snapshot', 'export_uuids');
CALL cubemaster_drop_column_if_exists('t_cube_pause_snapshot', 'export_uuids');

SELECT RELEASE_LOCK('cubemaster_migration_20260817193000_export_uuids');
