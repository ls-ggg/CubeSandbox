# S3 存储后端测试用例清单（含跨机）

面向 S3 backend 的功能与资源回归清单。**功能**和**资源**是两列独立结论：功能看状态与业务可用，资源看 mount／cubecow 对象／NVMe／包目录有没有残留，任何一列不过都算不过。

最后更新：2026-08-21。

## 0. 前置与约定

### 0.1 集群

| 角色 | 节点 | 说明 |
|---|---|---|
| control + compute | `21.130.217.64` | Master／CLI／SDK 从这里发起 |
| compute（模版节点） | `21.130.217.135` | 模版和源沙箱默认落在这里 |
| 数据面 | `s3lvol-f842bad` | 两台各一个进程，日志 `/data/log/rcow/s3lvol_tgt.log` |

`CUBE_API_URL=http://127.0.0.1:3000`（在 `.64` 上）。

### 0.2 模版

建模版必须带 envd 端口，否则 `commands.run` 一律 404：

```bash
cubemastercli cubebox template create-from-image \
  --image <image> --alias <alias> \
  --writable-layer-size 4Gi --backend s3 \
  --cpu 2000 --memory 2048 \
  --expose-port 49983 --probe 49983 --probe-path /health \
  --node 21.130.217.135 --interval 5s
```

`--backend s3` 决定了这个模版之后所有沙箱、快照、pause 包都走 S3。

### 0.3 访问与断言口径

- **必须用 SDK 访问沙箱**（`Sandbox.commands.run` / `run_code`），宿主 `ping` 沙箱 IP **不算**访问成功。
- **跨机恢复必须做业务级校验**：不能只看 Master 状态和 `echo`。跨机的典型失败形态是"VM restore 成功、状态 running、第一条命令还能返回，随后所有请求超时"——因为 memory 完整而磁盘是空洞的，第一条命令由 page cache 应答。推荐做法是在源沙箱里生成一份数据集并记下 sha256，恢复后重算比对，再写一个新文件证明磁盘可写。
- 默认资源断言：沙箱销毁后 `sb-<沙箱id>-*` 全部消失、`/data/cubelet/storage/s3` 下无沙箱 ext4 挂载、包目录随包删除。
- Pause 包不得出现在客户可见的 snapshot 列表里。

### 0.4 编号

`L` 基础生命周期 / `P` 同机 Pause／Resume / `S` 同机 Snapshot／FromSnap / `D` 删除与残留 / `X` 跨机 Pause／Resume / `XS` 跨机 Snapshot 创建 / `R` 异常与恢复 / `A` 自动化套件。

---

## 1. L — 基础生命周期

| # | 场景 | 预期（功能） | 预期（资源） | 最新结果 |
|---|---|---|---|---|
| L1 | 从 S3 模版创建沙箱 → `commands.run` → 销毁 | 创建成功、命令有输出、销毁返回成功 | 无新增 lvol；无沙箱 ext4 挂载 | 通过 |
| L2 | 建 S3 模版 | 模版 READY；`snapshot list --s3` 可见 | 产出 `tpl-<模版id>-rootfs` / `-memory-snap` / `s3-meta-<模版id>-snap`；**模版 rootfs 必须带 `export_uuid`** | 通过（2026-08-21） |
| L3 | 删除无引用的模版 | 删除成功 | 三个对象和包目录都清掉 | 待重跑（历史上被子快照挡住，见 D5） |
| L4 | 同一模版并发创建多个沙箱 | 都能创建并访问 | 各自 `sb-<id>-rootfs-gen0`，互不干扰 | 待重跑 |
| L5 | 冷启动（cubelet 重启后首次用模版建沙箱） | 创建成功 | catalog 从 metadata 盘或推导得到 | 通过（`3aafa4c2` 起） |

**L2 的模版 rootfs export 是跨机的前提**，不是可选项：沙箱 rootfs 是模版 rootfs 的子快照，reference 导出只会引用**已经 committed 的 chunk**，父层没导过就被静默跳过（不报错、不回退 copy）。实测同一节点相隔 3 分钟的对照：父层导过 = 80 object／74.9MB／3 layers，没导过 = 17 object／7.2MB／2 layers，后者导入端 `e2fsck` 直接 `Root inode is not a directory`。

---

## 2. P — 同机 Pause / Resume

| # | 场景 | 预期（功能） | 预期（资源） | 最新结果 |
|---|---|---|---|---|
| P1 | Create → Pause → Resume → Kill | Resume 后 `echo` 立刻通 | 无残留 | 通过（2026-08-20，cube-proxy 更新后） |
| P2 | Pause → Resume → Pause → Resume → Kill | 两轮都通 | 不得累积两条 pause rootfs | 待重跑 |
| P3 | Pause 后立刻 Resume（不 sleep） | 不受 CREATING／锁竞争影响 | 同上 | 待重跑 |
| P4 | Pause → 直接删除（不 Resume） | 删除成功，`GET` 返回 404 | pause 包目录 ＋ 三个对象都清掉 | 通过（busy 容忍后，2026-08-21） |
| P5 | Pause 后再 Pause（期望失败） | 报 130400 not running | 无副作用 | 通过 |
| P6 | running 上直接 Resume（期望失败） | 报 130400 | 无副作用 | 通过 |
| P7 | Pause 后 `snapshot list` | 不得出现 pause 包 | — | 通过 |
| P8 | Pause 后 `cubebox list` | 能看到 paused 行，带 `remote` 和 `pause_snap` 两列 | — | 通过（`e6f40630` 起） |
| P9 | Pause → cubelet 重启 → Resume | 恢复后文件仍在 | 重启不产生孤儿对象 | 待重跑 |
| P10 | Pause 期间保留网络策略 | Resume 后 egress 限制仍生效 | — | 见 A3 |

**Resume 后必须刷 proxy 路由**：Resume 会重新分配宿主端口（沙箱 IP 不变），Master 调 cube-proxy 的 `/admin/backend_cache/delete` 让旧映射失效。proxy 镜像过旧（无该路由）时会回 404，表现为 Resume 成功但访问 502／Connection refused，且缓存不会自己过期（SDK 重试一直在续 TTL）。

---

## 3. S — 同机 Snapshot / FromSnap

| # | 场景 | 预期（功能） | 预期（资源） | 最新结果 |
|---|---|---|---|---|
| S1 | running 沙箱 Snapshot → Kill | 快照 READY | 快照对象在，沙箱对象清掉 | 通过 |
| S2 | Snapshot → FromSnap 建新沙箱 → 两边 Kill | 新沙箱可访问，业务数据与源一致 | 两个沙箱各自的盘都清掉 | 待重跑 |
| S3 | Kill 原沙箱后再 FromSnap | 同上 | 同上 | 待重跑 |
| S4 | Pause → Resume → Snapshot | Resume 后能立刻做快照 | — | 待重跑 |
| S5 | Snapshot → Pause → Resume → Kill | 三步都成功 | 快照包和 pause 包各自独立清理 | 待重跑 |
| S6 | FromSnap 出来的沙箱再 Pause／Snapshot | 应当支持（子沙箱 rootfs 必须是 RW volume） | — | **失败**：`create_snapshot_from_volume` 拿到的是 snap 名而不是 volume |
| S7 | paused 状态下做 Snapshot（期望失败） | 报 130400 | 无副作用 | 通过 |
| S8 | DelSnap 后再 FromSnap（期望失败） | 报 catalog not found | — | 通过 |
| S9 | DelSnap 不影响仍在跑的原沙箱 | 原沙箱继续可用 | 只删快照对象 | 通过 |

---

## 4. D — 删除与残留

| # | 场景 | 预期 | 最新结果 |
|---|---|---|---|
| D1 | Kill running 沙箱 | 无 `s3-meta-<沙箱id>`、无 `sb-*-rootfs-gen*`、无 mount 泄漏 | 通过 |
| D2 | 删除 paused 沙箱 | pause 包目录、catalog、三个对象都清掉 | 通过（busy 容忍后） |
| D3 | 删除 runtime snapshot | 快照对象清掉，不误伤模版 | 通过 |
| D4 | 有子沙箱在跑时删快照 | 要么拒绝，要么删完不影响子沙箱 | 待重跑 |
| D5 | 删模版 | 三个对象清掉 | 历史上被 18 个遗留子快照挡住，需先清子快照 |
| D6 | 删除失败后重入 | 再次发起删除能走完 | 通过：`rcow_delete_lvol` 对 **snapshot** 报 busy 时打 WARN 视为成功，清理继续、包目录照删（对象在 S3 上泄漏）；**volume 的 busy 仍然报错**（卷跟沙箱走，busy 说明有活的使用者），export drain 的 busy 也仍然报错（那是导出没发生） |

**已知泄漏**：export 成功的对象 `deletable=NO`，每次 pause 会泄漏 rootfs ＋ memory 两个对象，需要 cubecow 暴露 `release_export` 才能真删。

---

## 5. X — 跨机 Pause / Resume

跨机靠 isolate 源节点（`cubemastercli node isolate <node-id>`）迫使调度离开；测完记得 `node unisolate`。前置条件：`backend=s3` 且 `remote_status=ready`；两节点 `cpuid_hash` 与 `host_kernel_release` 必须一致。

| # | 场景 | 预期（功能） | 预期（资源） | 最新结果 |
|---|---|---|---|---|
| X1 | A 机 Pause → B 机 Resume | Resume 落在 B；`echo` 通；**业务数据 sha256 与 pause 前一致**；guest 能继续写新文件 | B 上是 `sb-<沙箱id>-rootfs-gen0` ＋ `sb-<沙箱id>-memory`（沙箱私有）＋包级 metadata | **通过**（2026-08-21，`71e6551b`，resume 8.4s） |
| X2 | X1 之后源节点状态 | A 上不得残留 paused 行；`list --all` 只有一行 running；`Sandbox.connect` 可用 | — | 通过（`3752ddf3` 起，跨机后调 `DropOriginTombstone`） |
| X3 | 跨机 Resume 后销毁 | 销毁成功 | **B 上三块盘全删**、无 mount 泄漏、无 busy 告警 | 通过（2026-08-21 实测 CLM 回收后零残留） |
| X4 | A → B → 再回 A | 两次跨机都成功 | 每次只在当前节点留自己的盘 | 待重跑 |
| X5 | 跨机 Resume 后立刻在 B 机 Pause | 能再次 pause 并导出 | — | 待重跑 |
| X6 | `remote_status=inprogress` 时跨机（期望失败） | 报 `cannot restore cross-node` | — | 通过 |
| X7 | XFS backend 跨机（期望拒绝） | 明确拒绝，不得调用 export／fetch | — | 未测（本集群只有 S3） |

**X1 的资源归属**：跨机时 rootfs 和 memory 都是 `import_lvol` 出来的 RW volume，按沙箱命名、随沙箱销毁。**同机 Resume 的 memory 是包的只读快照**（`tpl-<包id>-memory-snap`），属于 pause 包、要服务后续创建，不登记也不随沙箱删——两者靠 `StorageInfo.ImportedMemoryVol` 区分（只有跨机真导入时才写值）。

**已知限制**：跨机之后源节点那份 pause 包（目录 ＋ 三个对象）没人回收。同机靠下次 Pause 的 GC，跨机下次 Pause 在新节点。S3 后端当前删不掉，属已知限制。

---

## 6. XS — 跨机 Snapshot 创建（FromSnap）

**当前整体阻塞**，先单列出来。

### 6.1 阻塞点：一个 export uuid 在同一节点只能 import 一次

用独立 index 对同一个 metadata uuid 连做两次 import：第一次出来的盘 `e2fsck` 全干净；第二次换个名字导同一个 uuid，s3lvol 直接回 `precondition failed: File exists`。**但它已经把空 lvol 建出来了**，cubecow 记进 index，cubelet 的容错分支据此判定"已存在"继续往下走，activate 拿到空盘，挂载报 `EXT4-fs error: inode #2: iget: checksum invalid`。

一次跨机 FromSnap 的 Create 里恰好有两次 metadata import：`EnsureRemotePackageLocal` 先按包名导一份用于读 catalog／spec，创建流程再按沙箱名导第二份。

因此：

- **Resume 不受影响**——一个 pause 包只服务一个沙箱，每个 uuid 正好导一次。
- **FromSnap 不成立**——同一快照要在该节点起第二个沙箱时，没有第二份 export 可导。

可行形态（待定）：每节点导一次 → 本地 `create_snapshot` 成 catalog 里的规范 `-snap` 名 → 每个沙箱按同机路径 clone，即让目标节点长得和源节点一样。

### 6.2 用例

| # | 场景 | 预期 | 状态 |
|---|---|---|---|
| XS1 | A 机 Snapshot → B 机 FromSnap → 业务校验 | 新沙箱可访问，数据与源一致 | **阻塞**（6.1） |
| XS2 | A 机 Snapshot → Kill 原沙箱 → B 机 FromSnap | 同上 | 阻塞 |
| XS3 | 同一快照在 B 机起**两个**沙箱 | 两个都能起，互不干扰 | 阻塞（6.1 的核心场景） |
| XS4 | B 机 FromSnap 出来的沙箱再 Pause／Snapshot | 支持 | 阻塞（叠加 S6） |
| XS5 | B 机 FromSnap 沙箱销毁 | 沙箱自己的盘删干净，包的对象保留 | 阻塞。历史失败：catalog 记 `rootfs_kind=snapshot` 而导入出来是 volume，删的时候走 `delete_snapshot` 报 `use delete_volume instead` |
| XS6 | 跨机 FromSnap 的 metadata 归属 | 子沙箱写自己的 metadata，不得写到父层 | 阻塞 |

---

## 7. R — 异常与恢复

| # | 场景 | 预期 | 最新结果 |
|---|---|---|---|
| R1 | 节点上有 s3 沙箱行时重启 cubelet | 正常启动，不得崩溃循环 | 通过（`19a3a329` 起：恢复遇 `ErrS3NotReady` 跳过并 WARN） |
| R2 | s3lvol 没起时启动 cubelet | **cubelet 启动不依赖 s3lvol**：xfs 沙箱服务不受影响，S3 请求返回"s3lvol 暂时不可用" | 待测 |
| R3 | s3lvol 起来后 | 后台每 5s 重试，就绪后自动跑初始化并补一次启动恢复 | 通过（代码在位，日志已升为 WARN 可见） |
| R4 | export 失败 | Master 记 `remote_status=failed`，客户 RPC 不失败；跨机被拒 | 通过（ghost uuid → failed） |
| R5 | 三个 export 并发撞 drain busy | 不应导致 uuid 永久 inprogress | 已用**串行 export**规避（临时改动，未提交） |
| R6 | catalog.json 读不到（包已 seal／cubelet 重启） | 按包 id 推导对象名重建 entry | 通过（`3aafa4c2`） |
| R7 | catalog 只有一份 | 主机侧不得出现影子 catalog | 通过（`e6d0f7d9`：新包主机侧 `metadata/` 是空目录） |
| R8 | cube-proxy 缓存刷新失败 | Resume 应当报错／标记降级，而不是静默成功 | 已改为返回错误 ＋ 重试 ＋ 识别 404 旧版（`c7d9db9c`） |
| R9 | cubecow index 与 s3lvol lvstore 脑裂 | base 应能重建 | **未修**：index 里还有 base 的 entry 就永远 `skip mkfs`，需手工清空 `entries` |

---

## 8. A — 自动化套件

宿主 `root@9.135.78.206:36000`，脚本在 `/root/CubeSandbox/scripts/`（不在仓库里）。

| # | 套件 | 内容 |
|---|---|---|
| A1 | `pause_scenario_stress.py` | 7 个用例：`pressure_concurrent` / `cycle_double` / `delete_paused` / `immediate_resume` / `no_snap_leak` / `no_exited` / `mixed_ops`。N=1 全跑 23 步 |
| A2 | `volume_pause_matrix.py` | A–G 七条路径 49 项：含两个沙箱共享 volume、paused 时删 volume 再 resume 等 |
| A3 | `pause_extra_cases.py` | `network_policy`（resume 后 egress 仍被限制）、`cubelet_restart`（pause → 重启 cubelet → resume，marker 文件仍在） |
| A4 | `hostdir_pause_smoke.py` | host-dir 绑定挂载在 pause／resume 前后可读可写 |
| A5 | sdk_compat live e2e | `pytest --run-e2e -m "lifecycle or pause"`。已知失败：`run_code` 走 Jupyter 49999，与 probe 49983 不一致，返回 502——不是存储问题 |
| A6 | 单测 | CubeMaster `pausesnap` / `restoreplace` / sandbox；Cubelet `storage` / `cubebox` / `runtemplate` / `cbri`。`Cubelet/storage` 需要 Linux ＋ `cubecow.h`，Darwin 上跑不全 |

---

## 9. 已知限制与常见坑

1. **模版 rootfs 必须导出**，否则跨机拿到的 rootfs 没有 ext4 元数据（见 L2）。
2. **一个 export uuid 每节点只能 import 一次**（见 6.1）。
3. **export 成功即 `deletable=NO`**，每次 pause 泄漏两个对象，需 cubecow 暴露 `release_export`。
4. **跨机后源节点的 pause 包无人回收**。
5. **metadata 导出恒走 copy 兜底**（`chunk 1 of layer 0 has no committed mapping`，源头是节点本地 base 的 mkfs 写从未 commit）。在 s3lvol `f842bad` 上数据是完整的，旧版 `c5215e4` 会缺块。
6. **cubelet 启动会删掉它不认识的 s3 lvol**，手工做实验时不要和 cubelet 共用 lvstore。
7. **日志级别在 boot 之后掉到 WARN**，INFO 全被丢掉；排查时临时 `curl` 节点的 `/debug/loglevel?level=info`，不要据"没有日志"推断卡死。
8. **`cubebox list` 默认只扫第一个节点**，paused 行是从 Master DB 补出来的，不受节点分页限制；`--hostid` 仍然生效。
