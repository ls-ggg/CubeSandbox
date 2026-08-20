# 跨机 Pause／Resume 与快照

## 能力概述

默认（XFS reflink）后端下，沙箱的可写盘、内存快照和模版副本都落在**所在节点的本地磁盘**上：暂停的沙箱只能在原节点恢复，运行时快照也只能在生成它的节点上派生新沙箱。一旦原节点被隔离、故障或资源打满，这些数据就跟着被困住。

S3 后端把这层数据放到**集群共享的对象存储（COS/S3）**上，由节点上的 `s3lvol` 数据面负责块设备与对象之间的读写。在此之上，Cube Sandbox 提供两项跨机能力：

| 能力 | 含义 | 沙箱 ID |
|---|---|---|
| **跨机 Resume** | 沙箱在 A 节点 `pause()`，之后在 B 节点恢复运行 | 不变 |
| **跨机快照建沙箱** | 在 A 节点生成的运行时快照（或由它提交出的模版），在 B 节点 `Sandbox.create()` | 新建 |

两者都不需要调用方感知节点：SDK 侧仍然是 `sandbox.pause()` / `Sandbox.connect(sandbox_id)` / `Sandbox.create(template=snapshot_id)`，落在哪个节点由 CubeMaster 决定。

```text
            ┌──────────────────────────────────────────┐
            │                CubeMaster                │
            │  restoreplace.Decide                     │
            │  1) 原节点优先                           │
            │  2) 原节点不可用 → 跨机候选              │
            │  准入：backend=s3 且 remote_status=ready │
            └────────┬────────────────────────┬────────┘
                     │ pause @ A              │ resume @ B
            ┌────────▼──────────┐  ┌──────────▼────────┐
            │ Node A            │  │ Node B            │
            │ Cubelet           │  │ Cubelet           │
            │ cubecow (s3)      │  │ cubecow (s3)      │
            │ s3lvol            │  │ s3lvol            │
            └────────┬──────────┘  └──────────┬────────┘
                     │ export_snapshot        │ import_lvol
                     │ （异步上传）           │ （凭 export_uuid）
            ┌────────▼────────────────────────▼────────┐
            │         COS / S3（全集群共享）           │
            │         同一 region + 同一 bucket        │
            └──────────────────────────────────────────┘
```

Pause 或打快照时，Cubelet 在原节点发起 `export_snapshot`，上传是**异步**的；CubeMaster 后台循环轮询上传进度，把结果写进快照记录的 `remote_status`。只有 `remote_status=ready` 的快照才允许离开原节点。

| `remote_status` | 含义 | 能否跨机 |
|---|---|---|
| `pending` | 尚未发起上传 | 否 |
| `inprogress` | 上传进行中 | 否 |
| `ready` | 对象已完整落到 S3 | **是** |
| `failed` | 上传失败 | 否（同机仍可 Resume） |
| 空 | XFS 后端 | 否 |

## 当前限制

### 1. 只有 S3 后端支持跨机，且由模版一次性决定

后端在**建模版时**通过 `--backend s3` 固定。此后该模版派生的一切——沙箱、pause 快照、运行时快照、由快照提交出的新模版——都继承同一个后端，调用方无法在创建沙箱或创建快照时覆盖（`snapshot create --backend` 会被 Master 忽略，以沙箱持久化的后端为准）。

因此：

- **XFS 模版永远不能跨机**，其快照的 `remote_status` 恒为空，跨机准入直接拒绝。
- 想要跨机，只能重新用 `--backend s3` 建一个模版。
- 目前**没有 XFS ↔ S3 的迁移工具**，存量 XFS 模版无法原地转换；迁移工具在后续规划中。

### 2. 删除能力尚不完善，需要自行确认引用清零

S3 对象之间存在父子引用（模版 rootfs → 沙箱快照 → 由快照派生的沙箱），且已 export 的快照在导出被释放前不可删除。当前**没有自动的引用计数回收**，删除一个仍被引用的对象会在节点侧失败并返回 `Device or resource busy`（`precondition_failed`）。

实际影响：

- 删除必须**自下而上**：先删由快照派生的沙箱和子快照，再删快照，最后才是模版。
- 删除前先确认对象可删：`cubebox snapshot list --s3` / `cubebox tpl list --s3` 会向原节点 Cubelet 查询 `DELETABLE` 列（见下文）。
- pause 包删除失败时，绑定记录不会消失，而是落成 `DELETE_FAILED` 并记录 `last_error`，在 `cubebox list` 里显示为 `paused(delete_failed)`。这条记录是残留包唯一的名字，请勿手工清掉；待引用释放后重新发起删除即可（删除是可重入的）。

### 3. 跨机调度要求目标节点与原节点 CPU、内核一致

恢复一份内存快照本质上是把 vCPU 状态搬到另一台物理机上继续执行，因此目标节点必须与原节点在两个维度上**严格相等**：

| 字段 | 含义 | 采集方式 |
|---|---|---|
| `cpuid_hash` | CPU 指令集特性（CPUID）的指纹 | Cubelet 读取 `/proc/cpuinfo` 计算，随心跳上报 |
| `host_kernel_release` | 宿主机内核版本（`uname -r`） | 同上 |

这两个字段在 pause／打快照的时刻被冻结到快照记录上（只冻结这两项），调度时用它们对节点注册表做精确匹配后再进入常规打分。CPU 厂商、CPU 型号、内核指纹、KVM API 版本、guest image 与 cube-agent 版本等也会上报，但**仅用于诊断展示，不参与调度拦截**。

换言之：同一批次、同一内核版本的机器之间可以互相跨机；混合 Intel／AMD 机型，或滚动升级期间内核版本不一致的节点之间不行。哪些节点彼此兼容可以直接看 `CPUID` 与 `KERNEL_REL` 两列：

```bash
cubemastercli node list
# NODE_ID  NODE_IP  ...  CPU_VENDOR  CPUID  KERNEL_REL  KERNEL_FP  KVM_VER  KVM_TAINT
```

表中的长指纹做了截断，需要完整值时加 `--json`。

被拒绝时的报错（Resume 返回错误码 `130598`，从快照创建沙箱返回 `130400`，文案一致）：

```text
# 快照还没传完
origin node <node-id> cannot schedule snap-xxx and snapshot cannot restore cross-node
  (backend=s3 remote_status=inprogress)

# XFS 模版
... cannot restore cross-node (backend=xfs remote_status=)

# 挂载了宿主机目录（host-mount），被钉在原节点
... cannot restore cross-node (host-mount)

# 没有 CPU／内核匹配的节点
no compatible node for cross-node restore of snap-xxx (cpuid_hash/kernel match)
```

此外，沙箱如果使用了 host mount 持久化目录，会被钉死在原节点，无论 `remote_status` 是否 ready 都不参与跨机。

### 4. 目标节点必须已有原始模版的副本

跨机搬过去的是**数据**——pause 包／快照包的 rootfs、内存和 metadata 都能凭 `remote_uuids` 从 S3 拉到目标节点。但沙箱启动还需要原始模版的 kernel 与 image 元数据，这部分不在 S3 包里，也不会跟着快照走。目标节点上没有这个模版的副本时，恢复会失败：

```text
ensure cube run template tpl-xxx failed: template tpl-xxx is not available locally
```

所以制作模版时就应该覆盖所有希望参与跨机的节点（`template create` 不加 `--node` 即全部健康节点），事后补可以用：

```bash
cubemastercli cubebox template redo tpl-xxx --node <target-node-id>
cubemastercli cubebox template info tpl-xxx   # 确认副本列表
```

## 安装与配置

### 一、节点侧：部署 s3lvol 并配置 S3 服务密钥

S3 密钥**不在** Cubelet 或 cubecow 的配置里，而属于节点上的 `s3lvol` 数据面。每台参与 S3 后端的计算节点都要单独配置，且**全集群必须指向同一个 region 与同一个 bucket**，否则 B 节点无法读到 A 节点上传的对象。

写入 `/data/cubelet/cos.cfg`：

```ini
[cos_config]
  secretid = "<SECRET_ID>"
  secretkey = "<SECRET_KEY>"
  region = "ap-nanjing"
  cos_endpoint = "cos.ap-nanjing.myqcloud.com"
  cos_bucket_name = ["cubecow-1253970226"]
```

该文件包含密钥，请设置为 `0600`。s3lvol 只在启动时读取它，并通过进程环境变量传给数据面，不会出现在命令行或日志中。

另外需要准备一块本地 WAL／元数据日志镜像（**不能从别的机器拷贝**，它的大小决定了日志布局，且日志内容属于本节点的 lvstore）：

```bash
mkdir -p /data/cubelet/rcow
truncate -s 512G /data/cubelet/rcow/wal_bdev.img
```

然后启动数据面（脚本幂等，重复执行只会提示已在运行）：

```bash
cd /usr/local/services/cubetoolbox/s3lvol-<version>
scripts/rcow_start.sh
```

常用可覆盖项（完整列表见该目录 `README.md`）：

| 环境变量 | 默认值 | 含义 |
|---|---|---|
| `RCOW_COS_CFG` | `/data/cubelet/cos.cfg` | 密钥配置路径 |
| `RCOW_WAL_IMG` | `/data/cubelet/rcow/wal_bdev.img` | WAL 镜像 |
| `RCOW_LVS_NAME` | `rcow` | lvstore 名，同时是对象在 S3 上的前缀 |
| `RCOW_RPC_SOCK` | `/var/run/s3lvol.sock` | JSON-RPC socket，Cubelet 由此接入 |

### 二、Cubelet：指向 s3lvol 的 socket

Cubelet 在 `storage_backend = "cubecow"` 时会同时启动两个 cubecow 句柄：reflink（XFS）与 s3。S3 句柄的初始化是**软失败**的——s3lvol 没起来时 Cubelet 照常启动，S3 相关操作返回 `s3 storage is not ready (initializing)`，待 socket 可用后自动恢复。

```toml
[plugins."io.cubelet.internal.v1.storage"]
  storage_backend = "cubecow"
  data_path = "/data/cubelet/storage"

  [plugins."io.cubelet.internal.v1.storage".cow.s3]
  socket_path = "/var/run/s3lvol.sock"
  # state_dir = "/data/cubelet/storage/s3"   # 省略则由 data_path 推导为 <data_path>/s3
  # rpc_timeout_ms = 10000
  # size_policy = "round_up"
```

| 键 | 默认值 | 说明 |
|---|---|---|
| `socket_path` | `/var/run/s3lvol.sock` | 必须与 `RCOW_RPC_SOCK` 一致 |
| `state_dir` | 由 `data_path` 推导 | cubecow 侧索引（`index.json`）目录 |
| `rpc_timeout_ms` | `10000` | 单次 RPC 超时 |
| `size_policy` | `round_up` | 容量换算策略，可选 `strict` |

### 三、制作模版时指定后端

```bash
cubemastercli cubebox template create-from-image \
  --image <registry>/<repo>/sandbox-code:latest \
  --alias my-s3-template \
  --backend s3 \
  --writable-layer-size 4Gi \
  --cpu 2000 --memory 2048 \
  --expose-port 49983 --probe 49983 --probe-path /health
```

`--backend` 取值 `xfs｜s3`，**省略即 xfs**（保持历史行为）。这是整条链路上唯一一次选择后端的机会。

模版就绪后，正常使用 SDK 即可，跨机对调用方透明：

```python
from cubesandbox import Sandbox

sb = Sandbox.create(template=TEMPLATE_ID, timeout=1800)
sb.commands.run("echo ok")
sb.pause()                                # 在当前节点打 pause 快照并异步上传
sb = Sandbox.connect(sb.sandbox_id)       # 可能在另一台节点上恢复
```

## cubemastercli 新增的列与 `--s3` 参数

### `cubebox list`：默认输出新增 `remote` 与 `pause_snap`

暂停的沙箱在节点上没有 shim 进程，节点扫描扫不到，因此这些行改由 CubeMaster 直接读 pause 绑定表补齐——**不受 `--index/--size` 节点分页限制**（分页只约束运行中沙箱的节点扫描），但 `--hostid` 仍然生效。

默认列：

```text
sandbox_id  status  backend  remote  pause_snap  host_id  create_at  pause_at
```

| 列 | 含义 |
|---|---|
| `status` | 异常的 pause 绑定状态会附在后面，如 `paused(delete_failed)` |
| `backend` | `xfs` 或 `s3` |
| `remote` | pause 快照的上传状态，跨机 Resume 需要 `ready`；非 S3 显示 `-` |
| `pause_snap` | pause 快照 ID，删除／排查残留时需要 |

`--wide` 在其后追加 `template_id`、`namespace`、`host_ip`、`labels`；`--all` 扫描全集群健康节点。

```bash
cubemastercli cubebox list --all
cubemastercli cubebox list --hostid <node-id> --wide
```

### `snapshot list` / `tpl list`：新增 `BACKEND`、`REMOTE_STATUS` 与 `--s3`

`cubebox snapshot list` 默认列：

```text
SNAPSHOT_ID  STATUS  SANDBOX_ID  NODE_ID  BACKEND  REMOTE_STATUS  CREATED_AT
```

`--output wide` 换成含 `DISPLAY_NAME`、`RUNTIME_REFS`、`LAST_ERROR` 的详细视图。`cubebox tpl list` 同样新增 `BACKEND` 列。

`--s3` 是给运维排查删除残留用的**临时开关**（后续版本会移除）：带上它之后，CLI 会按原节点分组，直连该节点 Cubelet 的 gRPC 端口发起一次 `Snapshot.BatchStatus`，在表尾追加两列：

| 列 | 含义 |
|---|---|
| `ORIGIN_IP` | 该快照／模版所在的原节点 IP |
| `DELETABLE` | rootfs 快照当前是否可删：`true`／`false`／`unknown`（查询超时或连不上）／`-`（非 S3） |

```bash
cubemastercli cubebox snapshot list --s3
cubemastercli cubebox tpl list --s3 --cubelet-port 9999
```

`--cubelet-port` 默认 `9999`，只在 `--s3` 场景下使用；单节点查询超时为 5 秒，超时的行显示 `unknown` 而不是报错中断。

## 调度仍然是本地优先

跨机是**兜底**，不是常态。恢复一个沙箱时，CubeMaster 的决策顺序是：

1. **先看原节点**：原节点健康、未被隔离（cordon）、在请求指定的调度范围内，并且用原节点做一次试调度能通过资源检查——满足则直接落回原节点，不走跨机。
2. **原节点不行才考虑跨机**：此时才检查跨机准入（`backend=s3` 且 `remote_status=ready`，且沙箱没有 host mount）。
3. **在兼容节点中调度**：用冻结的 `cpuid_hash` + `host_kernel_release` 精确筛出候选节点，排除原节点自身，再走常规调度打分。
4. 三步都不成立则拒绝，返回上文的 `cannot restore cross-node ...`。

真正走到第 3 步时，CubeMaster 会额外放行「模版必须在本地」这条常规调度约束，由目标节点按需从 S3 拉取所需对象。

这样设计的好处是：数据在原节点仍然是热的，本地恢复不需要从 S3 回读，跨机带来的额外读放大只在真正必要时才发生。运维上也有一个直接的推论——想强制某个沙箱迁走，隔离原节点即可：

```bash
cubemastercli node isolate <node-id>     # 之后的 Resume 会走跨机路径
cubemastercli node unisolate <node-id>   # 恢复调度
```

节点隔离的完整说明见[节点隔离](./node-isolation.md)。
