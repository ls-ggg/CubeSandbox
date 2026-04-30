# 升级到 PVM 模式

> ⚠️ **测试版警告**
>
> 当前 PVM 支持处于**早期测试阶段**，尚未在所有环境下完成充分验证。
> 升级操作涉及替换宿主机内核，**操作不当可能导致系统无法启动或现有沙箱环境损坏**。
>
> **强烈建议：**
> - 在非生产环境或测试机上操作，勿直接在线上环境执行
> - 操作前对重要数据和配置做好备份
> - 确保有物理控制台或带外管理（如 KVM/IPMI）访问权限，以便内核无法启动时恢复
> - 如遇问题，欢迎在 [GitHub Issues](https://github.com/TencentCloud/CubeSandbox/issues) 或 [Discord](https://discord.gg/kkapzDXShb) 反馈

PVM（半虚拟化模式）通过为宿主机和子机 VM 分别使用专用内核，提供更好的虚拟化性能。本文档介绍如何将现有 CubeSandbox 环境升级为 PVM 模式。

## 前提条件

- 已有 CubeSandbox 环境，或先参照[一键安装](./quickstart.md)完成部署
- 宿主机 root / sudo 权限
- x86_64 架构

---

## 第一步：安装 PVM 宿主机内核

PVM 宿主机内核为宿主机提供半虚拟化支持。根据发行版选择对应的包格式安装。

### RPM 系（RHEL、CentOS、TencentOS、Fedora）

```bash
# 下载安装包
wget https://github.com/TencentCloud/CubeSandbox/releases/download/v0.1.3-test-5/kernel-6.6.69_cube.pvm.host.005.x_gb85200d80fa2-1.x86_64.rpm
wget https://github.com/TencentCloud/CubeSandbox/releases/download/v0.1.3-test-5/kernel-headers-6.6.69_cube.pvm.host.005.x_gb85200d80fa2-1.x86_64.rpm

# 安装（若已有更高版本内核，加 --oldpackage 跳过版本检查）
rpm -ivh --oldpackage \
  kernel-6.6.69_cube.pvm.host.005.x_gb85200d80fa2-1.x86_64.rpm \
  kernel-headers-6.6.69_cube.pvm.host.005.x_gb85200d80fa2-1.x86_64.rpm
```

### DEB 系（Ubuntu、Debian）

```bash
# 下载安装包
wget https://github.com/TencentCloud/CubeSandbox/releases/download/v0.1.3-test-5/linux-image-6.6.69-cube.pvm.host.005.x-gb85200d80fa2_6.6.69-gb85200d80fa2-1_amd64.deb
wget https://github.com/TencentCloud/CubeSandbox/releases/download/v0.1.3-test-5/linux-headers-6.6.69-cube.pvm.host.005.x-gb85200d80fa2_6.6.69-gb85200d80fa2-1_amd64.deb

# 安装
dpkg -i \
  linux-image-6.6.69-cube.pvm.host.005.x-gb85200d80fa2_6.6.69-gb85200d80fa2-1_amd64.deb \
  linux-headers-6.6.69-cube.pvm.host.005.x-gb85200d80fa2_6.6.69-gb85200d80fa2-1_amd64.deb
```

### 设置 PVM 内核为默认启动项

**RPM 系：**

```bash
# 查看已安装的内核列表
grubby --info=ALL | grep -E "^kernel|^index"

# 将 PVM 内核设为默认（将 <index> 替换为 PVM 内核对应的序号）
grubby --set-default-index=<index>

# 确认默认内核
grubby --default-kernel
```

**DEB 系：**

```bash
# 查看已安装的内核
ls /boot/vmlinuz-*

# 更新 GRUB 配置
update-grub

# 设置 PVM 内核为默认启动项（根据实际内核版本字符串调整）
sed -i 's/^GRUB_DEFAULT=.*/GRUB_DEFAULT="Advanced options for Ubuntu>Ubuntu, with Linux 6.6.69-cube.pvm.host.005.x-gb85200d80fa2"/' /etc/default/grub
update-grub
```

### 重启进入 PVM 内核

```bash
reboot
```

重启后验证内核版本，并加载 PVM KVM 模块：

```bash
uname -r
# 预期输出：6.6.69-cube.pvm.host.005.x-gb85200d80fa2（或类似）

# 加载 PVM KVM 模块
modprobe kvm_pvm

# 确认模块已加载
lsmod | grep kvm
# 预期输出中包含 kvm_pvm
```

若需开机自动加载，执行：

```bash
echo 'kvm_pvm' > /etc/modules-load.d/kvm-pvm.conf
```

> **提示：** 如果已经通过一键部署安装了 CubeSandbox 环境，无需重新安装整套服务，直接重启进入 PVM 内核后继续执行第二步即可。

---

## 第二步：安装 PVM 子机内核

PVM 子机内核（`vmlinux-pvm`）在沙箱 VM 内部使用。下载后放置到 CubeSandbox 指定目录。

```bash
# 下载子机 vmlinux
wget https://github.com/TencentCloud/CubeSandbox/releases/download/v0.1.3-test-5/vmlinux-pvm

# 复制到 CubeSandbox 内核目录（重命名为 vmlinux）
cp vmlinux-pvm /usr/local/services/cubetoolbox/cube-kernel-scf/vmlinux
```

确认文件已就位：

```bash
ls -lh /usr/local/services/cubetoolbox/cube-kernel-scf/vmlinux
```

---

## 第三步：重新制作模版

更换子机内核后，需要重新制作模版，新建的沙箱才会使用 PVM 子机内核。

```bash
# 查看现有模版
cubemastercli template list

# 删除旧模版
cubemastercli template delete --template-id <your-template-id>

# 从 OCI 镜像重新创建模版
cubemastercli template create --image <your-image> --template-id <your-template-id>
```

详细的模版创建参数，参见[从 OCI 镜像创建模版](./tutorials/template-from-image.md)。

模版重建完成后，基于该模版新建的沙箱将自动使用 PVM 子机内核。

---

## 常见问题

### RPM 安装提示文件与自身冲突

```
file ... conflicts with file from package kernel-...-1.x86_64
```

这是 `binrpm-pkg` 在某些发行版下将同一文件写入多个子包导致的已知问题。使用 `--replacefiles` 强制覆盖安装：

```bash
rpm -ivh --oldpackage --replacefiles --replacepkgs \
  kernel-6.6.69_cube.pvm.host.005.x_gb85200d80fa2-1.x86_64.rpm
```

### RPM 提示已安装更高版本内核

```
package kernel-X.Y.Z (which is newer than ...) is already installed
```

使用 `--oldpackage` 跳过版本比较：

```bash
rpm -ivh --oldpackage kernel-6.6.69_cube.pvm.host.005.x_gb85200d80fa2-1.x86_64.rpm
```

### 重启后验证 PVM 是否生效

```bash
# 查看 dmesg 中的 PVM 初始化信息
dmesg | grep -i pvm

# 查看 KVM 模块
lsmod | grep kvm
```
