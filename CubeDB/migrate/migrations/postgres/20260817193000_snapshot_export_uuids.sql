-- Copyright (c) 2026 Tencent Inc.
-- SPDX-License-Identifier: Apache-2.0
--
-- Persist cubecow_export_snapshot uuids (PostgreSQL).

-- +goose NO TRANSACTION
-- +goose Up

SELECT cubemaster_acquire_migration_lock('cubemaster_migration_20260817193000_export_uuids', 60);

SELECT cubemaster_assert_table_exists('t_cube_snapshot');
SELECT cubemaster_assert_table_exists('t_cube_pause_snapshot');

SELECT cubemaster_add_column_if_missing('t_cube_snapshot', 'export_uuids', 'text');
SELECT cubemaster_add_column_if_missing('t_cube_pause_snapshot', 'export_uuids', 'text');

SELECT pg_advisory_unlock(hashtext('cubemaster_migration_20260817193000_export_uuids'));

-- +goose Down

SELECT cubemaster_acquire_migration_lock('cubemaster_migration_20260817193000_export_uuids', 60);

SELECT cubemaster_drop_column_if_exists('t_cube_snapshot', 'export_uuids');
SELECT cubemaster_drop_column_if_exists('t_cube_pause_snapshot', 'export_uuids');

SELECT pg_advisory_unlock(hashtext('cubemaster_migration_20260817193000_export_uuids'));
