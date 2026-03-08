[English](./README.md) | 简体中文

# cygctl

> [!IMPORTANT]
> **cygctl 不会安装、下载或配置 Cygwin 或 WSL。**
> 它是一个控制工具，用于在**已安装的 Cygwin 环境**中执行命令，提供类似 WSL 的接口。
> 如需安装 Cygwin，请访问 [cygwin.com](https://www.cygwin.com)。如需安装 WSL，请参阅 [Microsoft 文档](https://learn.microsoft.com/windows/wsl/install)。

一个面向 AI Agent 和开发者的 Cygwin WSL 风格命令行工具。

**cygctl 不是 shell alias，也不是 shim 垫片。** 简单的 `alias cyg='bash.exe'` 在涉及管道、退出码检测或用户切换时就会失效。cygctl 是一个专门构建的二进制工具，负责处理以下这些事：

- **正确的 stdio 连接** — stdin/stdout/stderr 被正确绑定，管道和重定向按预期工作
- **退出码传递** — 子进程的退出码会原样返回给调用方，保障脚本和 CI 环境的可靠性
- **进程生命周期管理** — 通过 Windows Job Object 和进程 API 枚举、查看并终止 Cygwin 进程
- **UAC 提权** — `sudo` 启动一个提权子进程并将其 I/O 桥接回来，这是任何 alias 都无法实现的
- **用户切换** — `su` 调用 `CreateProcessWithLogonW`，以另一个 Windows 账户身份启动进程
- **WSL 互操作** — 在 Cygwin 与 WSL 之间进行路径格式转换和跨环境命令调度
- **包管理** — 以 Go 重写的 `apt-cyg`，具备完整的依赖解析能力

WSL 风格的接口只是 UX 层，真正的价值在于它背后所做的一切。

## 功能特性

- **单一可执行文件** - 无依赖，直接放入 PATH 即可使用
- **WSL 风格接口** - WSL 用户熟悉的语法
- **AI Agent 友好** - 简单、可预期的命令结构
- **完整的标准输入/输出支持** - 正确的管道处理
- **退出码传递** - 脚本使用时返回正确退出码
- **包管理** - 以 Go 重写的 apt-cyg
- **UAC 提权** - sudo 用于 Windows 管理员任务
- **用户切换** - su 用于切换 Windows 用户账户

## 快速安装

```powershell
# PowerShell（一行命令）
irm https://raw.githubusercontent.com/chen0430tw/cygctl/master/install.ps1 | iex
```

安装完成后重启终端即可使用 `cyg` 和 `apt` 命令。

## 手动安装

```powershell
# 下载二进制文件到 Cygwin bin 目录
$bin = "C:\cygwin64\bin"
Invoke-WebRequest -Uri "https://github.com/chen0430tw/cygctl/releases/latest/download/cygctl.exe" -OutFile "$bin\cygctl.exe"
Invoke-WebRequest -Uri "https://github.com/chen0430tw/cygctl/releases/latest/download/apt-cyg.exe" -OutFile "$bin\apt-cyg.exe"
Invoke-WebRequest -Uri "https://github.com/chen0430tw/cygctl/releases/latest/download/sudo.exe" -OutFile "$bin\sudo.exe"
Invoke-WebRequest -Uri "https://github.com/chen0430tw/cygctl/releases/latest/download/su.exe" -OutFile "$bin\su.exe"

# 加入 PATH
[Environment]::SetEnvironmentVariable("PATH", "$bin;" + [Environment]::GetEnvironmentVariable("PATH", "User"), "User")
```

## 组成部分

| 文件 | 说明 |
|------|------|
| `cygctl.exe` | 主 CLI 工具 |
| `apt-cyg.exe` | 包管理器 |
| `sudo.exe` | UAC 提权 |
| `su.exe` | 切换 Windows 用户（通过 `CreateProcessWithLogonW`） |

## 使用方法

### WSL 命令对照

如果你熟悉 WSL，以下是 WSL 命令与 `cyg` 的对应关系：

| WSL | cyg |
|-----|-----|
| `wsl` | `cyg` |
| `wsl ls -la /tmp` | `cyg ls -la /tmp` |
| `wsl -e ls -la` | `cyg --exec "ls -la"` |
| `wsl --cd "D:\Projects" -e pwd` | `cyg --cd "D:\Projects" --exec "pwd"` |
| `wsl --shutdown` | `cyg --shutdown` |
| `wsl --status` | `cyg --status` |

### 基本命令

```bash
# 交互式 Shell
cyg

# 执行命令
cyg --exec "ls -la /cygdrive/c"
cyg -e "echo hello"

# 切换目录后执行
cyg --cd "D:\Projects" --exec "pwd"

# 直接传入命令（类似 wsl）
cyg ls -la /tmp
```

### 包管理（apt-cyg）

```bash
# 更新包列表
apt update

# 搜索包
apt search python

# 安装包
apt install vim git

# 列出已安装的包
apt list --installed

# 显示包信息
apt show bash

# 显示依赖关系
apt depends vim

# 升级包
apt upgrade

# 移除包
apt remove vim
```

### Sudo（UAC 提权）

```bash
# 以管理员权限执行命令
sudo netstat -an

# 编辑受保护的文件
sudo notepad C:\Windows\System32\drivers\etc\hosts
```

### Su（切换用户）

```bash
# 以其他 Windows 用户身份打开交互式登录 Shell
cyg --user alice
su alice

# 以其他用户身份执行单个命令
cyg --user alice --exec "whoami"
su alice whoami

# 切换目录后以其他用户身份打开 Shell
cyg --user alice --cd "D:\Projects"
```

> **注意：** `su` 使用 `CreateProcessWithLogonW`，需要 Windows 的 Secondary Logon 服务（`seclogon`）处于运行状态。该服务在所有现代 Windows 版本上默认启用。

### WSL 集成（cyg wsl）

`cyg wsl` 让你在 Cygwin 内管理和操作 WSL。

```bash
# 交互式启动默认 WSL 发行版
cyg wsl

# 列出所有 WSL 发行版（含状态与版本）
cyg wsl --list

# 转换路径格式（Windows / Cygwin / WSL）
cyg wsl --path "C:\Users\alice"
cyg wsl --path /cygdrive/c/Users/alice
cyg wsl --path /mnt/c/Users/alice
# 输出：windows=C:\Users\alice
#       cygwin=/cygdrive/c/Users/alice
#       wsl=/mnt/c/Users/alice

# 在默认 WSL 发行版中执行命令
cyg wsl --exec -- ls -la /tmp

# 在指定 WSL 发行版中执行命令
cyg wsl --exec Ubuntu -- whoami

# 关闭所有 WSL2 虚拟机
cyg wsl --shutdown
```

| 选项 | 简写 | 说明 |
|------|------|------|
| `--list` | `-l` | 列出发行版（含名称、状态、WSL 版本） |
| `--path <path>` | `-p` | 转换路径为 Windows / Cygwin / WSL 格式 |
| `--exec [distro] -- <cmd>` | `-e` | 在发行版中执行命令（省略则使用默认） |
| `--shutdown` | | 关闭所有 WSL2 虚拟机 |

### 状态与管理

```bash
cyg --status    # 显示 Cygwin 状态
cyg --shutdown  # 终止所有 Cygwin 进程
cyg --version   # 显示版本
cyg --help      # 显示帮助
```

## apt-cyg 命令列表

| 命令 | 说明 |
|------|------|
| `update` | 下载最新包列表 |
| `install <pkg...>` | 安装包（含依赖） |
| `remove <pkg...>` | 移除包 |
| `search <pattern>` | 搜索包 |
| `list [--installed]` | 列出所有或已安装的包 |
| `show <package>` | 显示包信息 |
| `depends <package>` | 显示依赖关系 |
| `rdepends <package>` | 显示反向依赖 |
| `upgrade [pkg...]` | 升级包 |
| `download <pkg...>` | 仅下载不安装 |
| `autoremove` | 查找未使用的依赖 |
| `clean` | 清除包缓存 |
| `mirror [url]` | 设置或显示镜像源 |

## install.ps1

安装脚本会自动配置：

1. **下载** - 从 GitHub Releases 获取二进制文件
2. **PATH** - 将 `C:\cygwin64\bin` 加入用户 PATH
3. **PowerShell** - 创建 `cyg` 和 `apt` 函数
4. **CMD** - 使用 AutoRun 创建 doskey 宏
5. **Git Bash** - 在 `.bashrc` 中添加别名
6. **Cygwin** - 在 `.bashrc` 中添加别名

## 构建

需要 Go 1.21 及以上版本

```bash
# 构建全部
make all

# 安装至 Cygwin bin
make install

# 清理
make clean
```

## 为什么需要 cygctl？

现有的 Cygwin 包装工具（如 mintty）是终端模拟器，而非命令行工具。cygctl 填补了这个空缺，为 Cygwin 提供类似 `wsl` 的接口，让以下使用场景更加便利：

- AI Agent 执行 Cygwin 命令
- 开发者编写 Cygwin 操作脚本
- CI/CD 流水线与 Cygwin 交互

## 许可证

MIT
