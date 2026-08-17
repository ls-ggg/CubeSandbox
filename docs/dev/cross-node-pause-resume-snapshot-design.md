# Design: Cross-Node Pause／Resume／Snapshot

> **Status:** Draft (working notes)  
> **Issue:** https://github.com/TencentCloud/CubeSandbox/issues/1197  
> **Last updated:** 2026-08-04  
> **Agent memory:** `.cursor/rules/cross-node-pause-resume-snapshot-design.mdc`

---

## 1. 整体方案与架构

### 1.1 存储方案边界（先读）

CubeSandbox 快照存储存在 **两套完全独立** 的方案，**其上的数据不可混用**：

| 方案 | 说明 | 本文是否展开 |
|------|------|----------------|
| **原有 XFS 方案** | 节点本地 XFS（reflink／cubecow local）落盘与恢复；仅服务本机路径 | **不展开**；沿用既有实现 |
| **新的 S3 方案** | 以 S3 兼容对象存储为数据面（按需加载）；**默认即集群共享**，支撑跨机 | **本文重点** |

硬约束：

1. **数据不可混用**：XFS 方案产出的 snapshot **不能**经 S3 路径 restore／按需加载消费；S3 方案下的 snapshot **不能**当作 XFS 本地 volume／本地快照复用。
2. **不是「本地形态升级成集群形态」**：不存在把同一份 XFS 数据「开启共享后变成 S3 副本、两边随便用」的模型。
3. **下文若无特别声明**，Pause／Resume／Snapshot／上传／跨机调度均指 **S3 方案** 内的控制面与数据面行为。

### 1.2 待决断问题

1. **CoW／S3 开启粒度**：做集群级开启（同一时刻只有一种生效），还是模版级开启（两种可同时存在）？
2. **S3 方案配置与启动**：是否由客户自行开启、自行配置？
3. **后台定期 sync**：存储在 S3 方案上的盘，是否默认由后台定期 sync 到 S3？

### 1.3 要解决什么

在 **S3 方案** 下，让 **Paused 沙箱** 和 **普通快照** 能在集群内换节点继续用：

> **跨机启动不等待完整下载：** 跨机 Resume 或基于普通快照启动新沙箱时，S3 方案 **按需加载**；目标节点只需完成启动所需的远程快照访问准备，无需等待整个 snapshot 下载完成。具体原理参见 [S3 按需加载设计（链接待补充）]()。  
> **跨机前置：** S3 方案默认具备集群共享能力；跨机前须确认该 snapshot 的 sync 已完成（`remote_ready`）。

| 场景 | 用户感知 | 调度 | 结果 |
|------|----------|------|------|
| **Pause → Resume** | 仍是同一 sandbox 的 Pause／Resume | **本机优先**；本机不可用/资源不足再跨机 | **同一 `sandboxID`**；Resume **成功后删除** pause-snap 的 **本地 + 远端（S3）+ 元数据** |
| **基于普通快照启新沙箱** | 现有 from-snapshot Create API，**不感知**是否跨机 | **本机优先**；资源不够再跨机 | **新 `sandboxID`**；普通快照 **保留**（本地与远端都不因启动而删） |

Pause 与普通快照在 **S3 方案内** 共用同一套 S3 按需加载工具链；控制面 **分两张表、分生命周期**。与原有 **XFS 方案** 分轨，互不消费对方数据。

### 1.4 系统架构

S3 方案 **默认即集群共享**（不再区分「本地形态／集群共享形态」）。跨机前确认 sync 完成（`remote_ready`）即可在兼容 Host 上 Resume／Create。

```text
┌────────────────────────────────────────────────────────────────────────────────────────┐
│ CubeMaster · S3 方案 Snapshot 调度                                                      │
│ 1. 本机优先                                                                             │
│ 2. 本机无法调度时，筛选符合条件的其他机器（须 remote_ready）                                   │
└─────────────────────┬─────────────────────────────────────────────┬────────────────────┘
                      │                                             │                     
                选择 Host A                                   选择 Host B                 
                      ▼                                             ▼                     
┌──────────────────────────────────────────┐  ┌──────────────────────────────────────────┐
│ Host A · Cubelet A                       │  │ Host B · Cubelet B                       │
│ S3 方案 · 按需加载                         │  │ S3 方案 · 按需加载                         │
│ Pause／Resume／Create（本机或跨机）      │  │ Pause／Resume／Create（本机或跨机）       │
│ sync → remote_ready 后可被跨机选中         │  │ sync → remote_ready 后可被跨机选中          │
└─────────────────────┬────────────────────┘  └─────────────────────┬────────────────────┘
                      │                                             │                     
                      └──────────────────────┬──────────────────────┘                     
                                             │                                            
                           ┌──────────────────────────────────┐                           
                           │ S3（集群共享数据面）                │                           
                           │ remote_ready · 按需加载           │                           
                           │ 查询 sync 状态                    │                           
                           └──────────────────────────────────┘                           
```

| 组件／模块 | 负责能力 |
|-------------|----------|
| **S3 模块** | 以S3类后端存储作为远端，对上层屏蔽对象存储细节。提供统一API与原有 **XFS 方案** **分轨**，数据不可混用 |
| **Shim** | pause方案改造，COW/S3方案都与常规snapshot方案对齐，pause后shim退出。 |
| **Cubelet** | 1. 区分现有的 **CoW／S3** 两套存储方案<br>2. 根据请求选择访问 **CoW** 或 **S3**<br>3. 增加查询接口：返回 S3 上的 snapshot 是否 sync 完成（`remote_ready`）<br>4. 增加 sync 接口：上层可主动发起 snapshot 的 sync<br>5. Pause 后销毁 Shim<br>6. 支持 CubeMaster **复用同一 `sandboxID`** Resume 沙箱<br>7. 提供删除 snapshot 的 API：snapshot 跨机 Resume 后，到原机器上删除 snapshot |
| **Master DB** | 持久化 sandbox、Pause 快照、普通快照和 sync 任务等元数据（S3 方案侧） |
| **CubeMaster** | <span style="color:#d0021b">**0. 存储方案的切换（CoW／S3）**</span><br>1. 异步 sync 任务，并提供任务状态查询（跨机前须 `remote_ready`）<br>2. 提供 API 供 Cubelet 上报 node 信息，用于装箱／调度筛选<br>3. 基于 S3 的 Pause／Snapshot 流程支持指定是否 sync 到远端<br>4. Snapshot 管理须区分 **CoW／S3**，以及是 **pause** 还是 **普通 snapshot**<br>5. Pause 沙箱管理（如 sandboxID、原 node、对应的 pause-snapshot）<br>6. 调度筛选：源机优先；源机无法调度时，在原有调度基础上叠加同机型、Cube 组件版本等因子筛选新机器 |
| **Redis Proxy／CubeProxy** | 维护 `sandboxID` 到当前 Host／endpoint 的流量映射；跨机 Resume 后更新到新节点 |
| **CubeAPI** | 对外提供 HTTP／OpenAPI，转发生命周期、快照与共享相关请求 |
| **SDK／Client** | 发起 Pause、Resume、Snapshot、sync 与查询任务状态等调用 |

### 1.5 场景一：Pause／Resume（含跨机）

```text
Running(sandboxID=S, 旧 Shim)
        │ Pause
        ▼
Paused(sandboxID=S, pause_snap)
  · 旧 Shim 退出
  · sandboxID 不变
        │
        │ sync（跨机前须完成 → remote_ready）
        ▼
sync 完成 ──► remote_ready
        │
        │ Resume（sandboxID 仍为 S）
        │ ① 本机优先
        │ ② 本机无法调度且已 remote_ready → 跨机 Resume
        ▼
Running(sandboxID=S, 新 Shim)
  · 启动新 Shim / 沙箱
  · sandboxID 不变
        │
        └── 成功后消费删除 pause-snap：
            本地副本（源+目标）+ 远端 + Pause 元数据
```


### 1.6 场景二：常规 Snapshot 跨机恢复（启新沙箱，用户不感知）

```text
CreateSnapshot ──► 普通 snapshot（local_ready）
                      │
                      │ sync（跨机前须完成 → remote_ready）
                      ▼
                 sync 完成 ──► remote_ready
                      │
                      │ Create-from-snapshot（新 sandboxID）
                      │ ① 本机优先
                      │ ② 本机无法调度且已 remote_ready → 跨机 Resume
                      ▼
                   Running（新 sandboxID）
                      │
                      └── 普通 snapshot 保留（本地 + 远端都不因启动而删）
```

---

## 2. Snapshot 管理

> 本章按 **类型 × 存储方案** 正交梳理 snapshot，并给出创建／查询／销毁能力与简要流程。细节实现见后续各层章节。

### 2.1 类型正交：kind × 存储方案

Snapshot 用两个独立维度描述（**正交、不可混用数据**），组合共 4 类：

| 类型 | kind | 存储方案 | 说明 |
|------|------|----------|------|
| **A. CoW · pause** | `pause` | CoW | 本机 Pause 落盘；**不**走 S3 sync／跨机 |
| **B. CoW · normal** | `normal` | CoW | 本机显式 Snapshot；**不**走 S3 sync／跨机 |
| **C. S3 · pause** | `pause` | S3 | Pause 落盘 + 可选 sync；供 **同一 `sandboxID`** Resume（可跨机） |
| **D. S3 · normal** | `normal` | S3 | 显式 Snapshot + 可选 sync；可 **启新沙箱**（可跨机） |


### 2.2 能力列表（创建／查询／销毁）

图例：✅ 支持 · ❌ 不支持 · — 不适用

| 能力 | A. CoW·pause | B. CoW·normal | C. S3·pause | D. S3·normal |
|------|:------------:|:-------------:|:-----------:|:------------:|
| **创建** | ✅ Pause API | ✅ Snapshot API | ✅ Pause API（可指定是否 sync） | ✅ Snapshot API（可指定是否 sync） |
| **查询** | ✅ 挂在 sandbox `Paused`／内部元数据；**不进**用户普通快照列表 | ✅ 普通快照列表／详情 | ✅ 同左 + sync／`remote_ready` 状态 | ✅ 同左 + sync／`remote_ready` 状态 |
| **销毁** | ✅ Resume 成功后删净；或 Delete Paused 级联 | ✅ Delete Snapshot API | ✅ Resume 成功后删本地+远端+元数据；或 Delete Paused 级联 | ✅ Delete Snapshot（本地；若已 `remote_ready` 则含远端） |
| sync 到远端 | ❌ | ❌ | ✅（创建时指定或事后发起） | ✅（创建时指定或事后发起） |
| 跨机消费 | ❌ | ❌ | ✅ Resume 同 `sandboxID`（须 `remote_ready`） | ✅ 启新沙箱（须 `remote_ready`） |
| 用 pause-snap 启新沙箱 | ❌ | — | ❌ | — |

### 2.3 创建流程（简图 · S3）

S3 方案下 pause／normal 创建路径相同（本地落盘后可选 sync）；差异在 Pause 后 shim 退出、普通 Snapshot 后沙箱继续 Running。

```text
Client           Master              Cubelet（源节点）         S3
  │ Pause／Snapshot │                    │                     │
  │  (± sync 开关)  │                    │                     │
  │────────────────>│  本地 snapshot     │                     │
  │                 │───────────────────>│                     │
  │                 │  local_ready       │                     │
  │                 │<───────────────────│                     │
  │  成功 (±jobID)  │                    │                     │
  │<────────────────│                    │                     │
  │                 │                    │                     │
  │            opt 需 sync               │                     │
  │                 │  sync 任务         │                     │
  │                 │───────────────────>│  sync／上传至 S3    │
  │                 │                    │────────────────────>│
  │                 │                    │<────────────────────│
  │  查 job／状态   │  remote_ready      │                     │
  │<───────────────>│                    │                     │
```

### 2.4 销毁流程（简图 · S3）

CubeMaster 根据元数据找到 **snapshot 所在原节点**，对该节点 Cubelet 下发删除；**Cubelet 负责删除本机本地副本，并删除 S3 远端数据**（若已 sync／存在远端对象）。Master 再清理自身元数据。

#### 2.4.1 pause-snap 销毁（Resume 成功后消费）

```text
Client          Master                    Cubelet（原节点）              S3
  │ Resume 成功  │                           │                           │
  │  → Running   │                           │                           │
  │              │ 删除 pause-snap           │                           │
  │              │（调 snapshot 原节点）     │                           │
  │              │──────────────────────────>│  1) 删本地副本            │
  │              │                           │  2) 删 S3 远端            │
  │              │                           │      删除远端对象         │
  │              │                           │──────────────────────────>│
  │              │                           │<──────────────────────────│
  │              │ 删 Pause 元数据／解绑     │                           │
```

> Resume **失败不删** pause-snap。若跨机 Resume 后目标节点另有本地副本，Master 另调该节点只删本地（远端仍由原节点 Cubelet 删除）。Delete Paused 走级联删除（含进行中的 sync 任务）。

#### 2.4.2 normal snapshot 销毁

```text
Client              Master                    Cubelet（原节点）              S3
  │ Delete Snapshot  │                           │                           │
  │─────────────────>│  删除 snapshot            │                           │
  │                  │（调 snapshot 原节点）     │                           │
  │                  │──────────────────────────>│  1) 删本地副本            │
  │                  │                           │  2) 删 S3 远端            │
  │                  │                           │      删除远端对象         │
  │                  │                           │──────────────────────────>│
  │                  │                           │<──────────────────────────│
  │                  │ 删普通快照元数据          │                           │
  │  成功            │                           │                           │
  │<─────────────────│                           │                           │
```

> 基于普通快照 **启新沙箱成功后不删** 该 snapshot（与 pause 消费模型不同）。

---

## 3. CubeMaster 调度原理

> **Pause／Resume** 与 **基于 snapshot 创建沙箱** 共用同一套调度判定；差异仅在于消费对象（pause-snap 保同一 `sandboxID`；普通 snap 启新 `sandboxID`）与成功后是否删除 snap，**不**另建调度分支。

### 3.1 判定流程（总览）

调度目标：在满足数据可达与兼容约束的前提下，**优先原机**；仅当原机不可用或不健康时，才进入跨机筛选。

```text
                    收到 Resume 或 Create-from-snapshot
                                   │
                                   ▼
                    ┌──────────────────────────────┐
                    │ 1. snapshot 存储方案？        │
                    │    CoW 或 S3                  │
                    └──────────────┬───────────────┘
                                   │
              ┌────────────────────┴────────────────────┐
              │ CoW                                     │ S3
              ▼                                         ▼
     调度到原机器（结束）                ┌──────────────────────────────┐
                                         │ 2. 是否开启远程 S3 sync？    │
                                         └──────────────┬───────────────┘
                                                        │
                                   ┌────────────────────┴────────────────────┐
                                   │ 否                                      │ 是
                                   ▼                                         ▼
                          调度到原机器（结束）              ┌──────────────────────────────┐
                                                            │ 3. sync 是否完成？           │
                                                            │    remote_ready？            │
                                                            └──────────────┬───────────────┘
                                                                           │
                                                      ┌────────────────────┴────────────────────┐
                                                      │ 否                                      │ 是
                                                      ▼                                         ▼
                                             调度到原机器（结束）              ┌──────────────────────────────┐
                                                                               │ 4. 原机负载是否健康？         │
                                                                               └──────────────┬───────────────┘
                                                                                              │
                                                                         ┌────────────────────┴────────────────────┐
                                                                         │ 是                                      │ 否
                                                                         ▼                                         ▼
                                                                调度到原机器（结束）              5. 跨机调度筛选
                                                                                                         │
                                                                                                         ▼
                                                                                              见 §3.2
```

### 3.2 跨机调度筛选（步骤 5）

仅当步骤 1–4 均指向「可以／必须离开原机」时进入。在现有调度器之上叠加过滤条件，选出目标节点后，再走跨机 Resume／Create（S3 按需加载，不等待完整下载）。

```text
                    候选节点集合（集群内其它机器）
                                   │
                                   ▼
                    ┌──────────────────────────────┐
                    │ 过滤：负载健康               │
                    └──────────────┬───────────────┘
                                   ▼
                    ┌──────────────────────────────┐
                    │ 过滤：同机型                 │
                    └──────────────┬───────────────┘
                                   ▼
                    ┌──────────────────────────────┐
                    │ 过滤：CPU hash 与 kernel 匹配 │
                    │ （与原机 cpuid_hash、          │
                    │  host_kernel_release 一致）    │
                    └──────────────┬───────────────┘
                                   ▼
                    ┌──────────────────────────────┐
                    │ 过滤：Cube 组件版本匹配      │  ← TODO（见 §3.3）
                    └──────────────┬───────────────┘
                                   ▼
                    ┌──────────────────────────────┐
                    │ 过滤：S3 能力已开启          │  ← TODO（见 §3.3）
                    └──────────────┬───────────────┘
                                   ▼
                    在剩余节点上按既有调度策略择优
                                   │
                                   ▼
                    目标节点：RestoreRemote（按需访问）→ Start／Create
```

要点：

- **Pause／Resume** 与 **from-snapshot Create** 走同一决策树；跨机开关与兼容规则共用。
- 步骤 2／3 保证：没有远端副本或副本未就绪时，**绝不跨机**，避免不可恢复。
- 步骤 4／5 保证：有远端就绪时仍 **原机优先**；跨机是原机扛不住时的兜底。
- 跨机筛选须匹配原机 `cpuid_hash` 与 `host_kernel_release`（另有目标节点 `kvm_module_taint` 门闸）；Cube 组件版本与 S3 能力过滤见 §3.3 TODO。

### 3.3 TODO 与风险提示

下列过滤项标记为 **TODO**，实现排期存在不确定性：

| 项 | 含义 | 风险 |
|----|------|------|
| **Cube 组件版本匹配** | 目标节点 shim／kernel／guest-image 等与 snapshot 固化版本一致 | 若未按期落地：跨机可能落到不兼容节点，Resume／Create 失败或行为未定义 |
| **S3 能力开启** | 目标节点已具备 S3 方案等跨机恢复能力 | 若未按期落地：可能调度到无 S3 能力的节点，按需加载／恢复失败 |

**客户侧临时保障（文档指导，非代码兜底）：**

- 在上述 TODO 完成前，**由运维／客户自行保证**：参与跨机调度的节点机型一致、Cube 组件版本对齐，且均已正确开启并配置 S3 方案。
- 产品文档需写明：未满足时跨机 **不保证成功**；建议仅在同构、已验证 S3 就绪的节点池内开启跨机，或暂时关闭跨机、仅原机 Resume／Create。

---

## 4. 外部依赖

### 4.1 CubeCow 统一存储 API

**S3 存储方案与原有 XFS（reflink）方案统一实现在 CubeCow 内**，对 Cubelet **只暴露一套 API**。Cubelet **不**直接对接 S3 客户端或旁路工具链；选后端、本地落盘、远端 sync、按需加载与跨节点副本清理，均由 CubeCow 在统一接口下完成。

#### 依赖的 CubeCow API（在现有集合之上扩展）

下列为 Cubelet 依赖的 CubeCow 能力面（命名对齐现有 `cubecow_*` FFI；具体签名以实现为准）：

| 类别 | API | 说明 |
|------|-----|------|
| 生命周期 | `init`／`shutdown`／`reset_node_storage` | 引擎初始化与节点存储复位 |
| Volume | `create_volume`／`delete_volume`／`resize_volume`／`get_volume_info`／`get_volume_block_info`／`list_volumes` | 卷 CRUD／查询 |
| Volume 激活 | `activate_volume`／`deactivate_volume` | 挂载设备路径供沙箱使用 |
| Snapshot | `create_snapshot`／`delete_snapshot`／`list_snapshots` | 快照创建／删除／列举 |
| 可观测 | `get_metrics` | 节点存储指标 |

**相对现有 API 的增量约定：**

1. **创建类接口增加 `type`（后端类型）**  
   至少覆盖 `create_volume`、`create_snapshot`（及其它「创建」语义入口）。调用方指定后端，例如 `xfs`｜`s3`（枚举名以实现为准）。  
   - `type=xfs`：走原有本地 XFS／reflink 路径（本机语义）。  
   - `type=s3`：走 S3 方案（本地工作副本 + 集群共享数据面）。  
   同一 snapshot／volume **不得**跨 `type` 混用（与 §1.1 数据不可混用一致）。

2. **新增 `sync`：触发实时上传**  
   对指定 snapshot（或约定对象）发起 **实时 sync** 到远端 S3，供控制面在 Pause／Snapshot 后（或单独 sync 请求时）调用。成功后该对象具备跨机所需的远端就绪语义（控制面侧仍记为 `remote_ready`）。

3. **新增 `status`：查询 snapshot 上传状态**  
   查询指定 snapshot 当前 sync／上传进度与结果（如未开始／进行中／已完成／失败等；字段以实现为准）。Cubelet／Master 用其驱动 Job 状态与跨机门闸，**不**在 Cubelet 旁路探测 S3。

4. **删除语义增强（本地 + 远端 + 他节点副本）**  
   `delete_snapshot`（及必要时 `delete_volume`）须支持 **一次调用完成**：  
   - 删除 **本机本地副本**；  
   - 删除 **远端 S3 对象**（若已 sync／存在）；  
   - 删除 **其它节点上的本地副本**（若曾按需加载或缓存）。  
   若对象仍被引用（他节点激活中、引用计数未清零等），由 **CubeCow 存储内部闭环处理**（引用计数／延迟回收／拒绝或排队删除等），**不**要求 Cubelet／CubeMaster 逐节点编排删副本。

> Cubelet 职责收敛为：按 Master 下发选择 `type`、调用统一 API（创建／激活／`sync`／`status`／删除）、上报结果；跨机按需访问与副本回收细节留在 CubeCow。

### 4.2 CubeOps 节点与组件信息

> **备注（理想 vs 本期）：** 本节描述的是 **理想形态**——调度与 compat 所需的机器信息、负载、机型、Cube 组件版本统一经 CubeOps 查询。**本期实现**可能由 **Cubelet／CubeMaster 自行上报** 临时支撑上述字段；待 CubeOps 侧能力对齐后再收敛到本节约定，不改变 §3 过滤语义。

跨机 Pause／Resume／from-snapshot Create 的调度筛选（§3）所需的 **机器信息、负载、机型、Cube 组件版本** 等，由 **CubeMaster 查询 CubeOps** 获取；**不**新建专用兼容性 RPC，复用现有节点／集群查询能力。

#### 依赖目的

| 调度用途（见 §3） | 需要的信息 | 说明 |
|-------------------|------------|------|
| 原机是否可继续调度 | 节点健康、负载／可分配资源 | 步骤 4「原机负载是否健康」 |
| 跨机候选过滤：负载 | 各节点健康、allocatable／饱和度等 | §3.2 过滤「负载健康」 |
| 跨机候选过滤：同机型 | 节点机型（`instanceType` 或等价字段） | 与 snapshot 固化机型一致；缺则按跨机开启策略失败（见下） |
| 跨机候选过滤：组件版本 | shim／kernel／guest-image 等版本 | 与 snapshot 固化版本匹配；§3.3 标为 TODO 的落地仍依赖此处可查 |
| Pause／Snapshot 固化 compat | 源节点机型 + 组件版本 | 写入 snap 元数据，供后续 Resume／Create 比对 |

#### 复用的 CubeOps 查询面（不新增接口形态）

以现有 CubeOps 集群／节点 API 为准（Master 可经 CubeOps，或经其背后同源的节点清单；**不以 Cubelet 自报代替**）：

| 能力 | 典型入口（现网） | Master 取用字段（概念） |
|------|------------------|-------------------------|
| 节点列表／详情 | `GET /api/v1/nodes`、`GET /api/v1/nodes/{nodeID}` | `nodeID`／`hostIP`、`healthy`、`capacity`／`allocatable`、负载饱和度、`instanceType`、`versions[]`（`component`＋`version`／commit 等） |
| 集群版本矩阵（可选） | `GET /api/v1/cluster/versions` | 组件版本在节点间的分布，便于排查／批量过滤 |

字段名以实现为准；调度侧抽象为：**机器身份、健康与负载、机型、Cube 组件版本集合**。

#### 使用约定

1. **谁查**：CubeMaster 在 Pause（固化 compat）、Resume／Create-from-snapshot（调度过滤）时查询；Cubelet **不**直连 CubeOps 做兼容裁决。
2. **不扩 RPC**：不增加「QueryCompat」类专用接口；在现有 nodes／versions 上过滤即可。
3. **跨机开启时缺信息则失败**：若已开启跨机，而 CubeOps 无法提供某候选／源节点的 **机型** 或调度所需的 **组件版本**，Pause 固化或跨机调度 **直接报错**，不得静默跳过过滤（与 §3.3「未落地则跨机不保证」的产品兜底区分：此处是查询缺失，属硬失败）。
4. **与 §3.3 TODO 的关系**：组件版本匹配、S3 能力过滤的 **产品策略** 仍见 §3.3；本节只固定 **数据从 CubeOps 来、Master 负责过滤**。


