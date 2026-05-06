// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

use crate::log::Log;
use crate::{common::CResult, errf, infof, sandbox::sb::SandBox};
use cube_hypervisor::config::RestoreConfig;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::path::PathBuf;

// ── annotation keys ──────────────────────────────────────────────────────────

/// Identifies the update action to perform.
///
/// Supported values: `"RollbackSnapshot"`
const ANNO_UPDATE_EXT_ACTION: &str = "cube.shimapi.update.action";

/// (RollbackSnapshot) **Required.** URL of the snapshot to restore from.
/// Format follows `RestoreConfig::source_url`, e.g. `file:///data/snapshots/foo`.
const ANNO_ROLLBACK_SNAPSHOT_URL: &str = "cube.shimapi.update.rollback.snapshot_url";

/// (RollbackSnapshot) **Optional.** JSON-encoded `RollbackRestoreExt` carrying
/// additional backend-file descriptors (disks, fs, net, pmem, …) that should
/// replace the running VM's devices after the rollback.
///
/// Only the fields present in the JSON are applied; absent fields keep their
/// defaults (i.e. hypervisor re-uses the device config baked into the snapshot).
const ANNO_ROLLBACK_RESTORE_EXT: &str = "cube.shimapi.update.rollback.restore_config";

// ── supplemental restore fields ───────────────────────────────────────────────

/// Subset of `RestoreConfig` that callers may supply via annotation.
/// Using a dedicated struct keeps the wire format stable even if upstream
/// `RestoreConfig` gains new fields.
#[derive(Debug, Default, Serialize, Deserialize)]
struct RollbackRestoreExt {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub disks: Option<Vec<cube_hypervisor::vm_config::DiskConfig>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub net: Option<Vec<cube_hypervisor::vm_config::NetConfig>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub fs: Option<Vec<cube_hypervisor::vm_config::FsConfig>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub pmem: Option<Vec<cube_hypervisor::vm_config::PmemConfig>>,
}

// ── action implementations ────────────────────────────────────────────────────

/// Roll back the running VM to a previously-taken snapshot.
///
/// Steps:
/// 1. Pause the current VM and snapshot its state to a temporary path on disk
///    (so the hypervisor process can be cleanly detached).
/// 2. Resume the VM from the `snapshot_url` supplied by the caller, optionally
///    replacing block/network/fs devices via `restore_config`.
async fn do_rollback_snapshot(
    sb: &mut SandBox,
    annos: &HashMap<String, String>,
    log: &Log,
) -> CResult<()> {
    // --- parse snapshot_url (required) ---
    let snapshot_url = annos
        .get(ANNO_ROLLBACK_SNAPSHOT_URL)
        .ok_or_else(|| format!("missing annotation: {}", ANNO_ROLLBACK_SNAPSHOT_URL))?;

    infof!(
        log,
        "rollback snapshot: target snapshot_url={}",
        snapshot_url
    );

    // --- parse optional extra restore fields ---
    let ext: RollbackRestoreExt = match annos.get(ANNO_ROLLBACK_RESTORE_EXT) {
        Some(raw) => serde_json::from_str(raw)
            .map_err(|e| format!("invalid {}: {}", ANNO_ROLLBACK_RESTORE_EXT, e))?,
        None => RollbackRestoreExt::default(),
    };

    // --- build RestoreConfig ---
    let restore_config = RestoreConfig {
        source_url: PathBuf::from(snapshot_url),
        disks: ext.disks,
        net: ext.net,
        fs: ext.fs,
        pmem: ext.pmem,
        ..Default::default()
    };

    // --- delegate to sb ---
    sb.rollback_vm(restore_config).await.map_err(|e| {
        errf!(log, "rollback snapshot failed: {}", e);
        e
    })?;

    infof!(log, "rollback snapshot: finished");
    Ok(())
}

// ── public router ─────────────────────────────────────────────────────────────

pub async fn update_route(
    sb: &mut SandBox,
    annos: &HashMap<String, String>,
    log: &Log,
) -> CResult<()> {
    let action = match annos.get(ANNO_UPDATE_EXT_ACTION) {
        Some(a) => a.as_str(),
        None => return Ok(()), // no extended action requested
    };

    match action {
        "RollbackSnapshot" => do_rollback_snapshot(sb, annos, log).await,
        unknown => Err(format!("unknown update ext action: {}", unknown).into()),
    }
}
