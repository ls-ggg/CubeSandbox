-- Copyright (c) 2026 Tencent Inc.
-- SPDX-License-Identifier: Apache-2.0
--
-- Persist CoW backend (xfs|s3) on the running-sandbox spec row. Template
-- definition already has storage_backend; snapshot / pause-snapshot already
-- have backend. Empty historical rows mean xfs.

-- +goose NO TRANSACTION
-- +goose Up

CALL cubemaster_acquire_migration_lock('cubemaster_migration_20260817110000_sbx_be', 60);

CALL cubemaster_assert_table_exists('t_cube_sandbox_spec');

CALL cubemaster_add_column_if_missing(
  't_cube_sandbox_spec',
  'backend',
  "varchar(16) NOT NULL DEFAULT '' COMMENT 'xfs or s3; empty means xfs'"
);

CALL cubemaster_add_index_if_missing(
  't_cube_sandbox_spec',
  'idx_sandbox_spec_backend',
  "ADD INDEX `idx_sandbox_spec_backend` (`backend`)"
);

SELECT RELEASE_LOCK('cubemaster_migration_20260817110000_sbx_be');

-- +goose Down

CALL cubemaster_acquire_migration_lock('cubemaster_migration_20260817110000_sbx_be', 60);

CALL cubemaster_drop_index_if_exists('t_cube_sandbox_spec', 'idx_sandbox_spec_backend');
CALL cubemaster_drop_column_if_exists('t_cube_sandbox_spec', 'backend');

SELECT RELEASE_LOCK('cubemaster_migration_20260817110000_sbx_be');
