-- Copyright (c) 2026 Tencent Inc.
-- SPDX-License-Identifier: Apache-2.0
--
-- Persist CoW backend (xfs|s3) on the running-sandbox spec row (PostgreSQL).

-- +goose NO TRANSACTION
-- +goose Up

SELECT cubemaster_acquire_migration_lock('cubemaster_migration_20260817110000_sbx_be', 60);

SELECT cubemaster_assert_table_exists('t_cube_sandbox_spec');

SELECT cubemaster_add_column_if_missing('t_cube_sandbox_spec', 'backend', 'varchar(16) NOT NULL DEFAULT ''''');

CREATE INDEX IF NOT EXISTS idx_sandbox_spec_backend ON t_cube_sandbox_spec (backend);

SELECT pg_advisory_unlock(hashtext('cubemaster_migration_20260817110000_sbx_be'));

-- +goose Down

SELECT cubemaster_acquire_migration_lock('cubemaster_migration_20260817110000_sbx_be', 60);

SELECT cubemaster_drop_index_if_exists('t_cube_sandbox_spec', 'idx_sandbox_spec_backend');
SELECT cubemaster_drop_column_if_exists('t_cube_sandbox_spec', 'backend');

SELECT pg_advisory_unlock(hashtext('cubemaster_migration_20260817110000_sbx_be'));
