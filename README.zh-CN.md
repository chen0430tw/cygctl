[English](./README.md) | 简体中文

# cygctl

**cygctl** 是一个 Windows 命令行工具，让你在 PowerShell、CMD 或 Git Bash 中以与 `wsl` 相同的方式调用 Cygwin 命令，无需打开 Cygwin 自带的终端模拟器。

> [!IMPORTANT]
> **cygctl 不会安装、下载或配置 Cygwin 或 WSL。**
> 它是一个控制工具，用于在**已安装的 Cygwin 环境**中执行命令，提供类似 WSL 的接口。
> 如需安装 Cygwin，请访问 [cygwin.com](https://www.cygwin.com)。如需安装 WSL，请参阅 [Microsoft 文档](https://learn.microsoft.com/windows/wsl/install)。

## 为什么是 Cygwin 而不是 WSL？

简短回答：如果你能用 WSL，就用 WSL。cygctl 是为那些用不了或不想用 WSL 的场景准备的。

| | Cygwin + cygctl | WSL 2 |
|---|---|---|
| **进程模型** | 原生 Windows 进程 —— 在任务管理器中可见，继承 Windows 环境变量，可用任何 Windows 工具管理 | 运行在 Hyper-V 虚拟机内的 Linux 进程 |
| **文件系统** | 直接操作 NTFS 路径（`C:\...`），继承 Windows ACL | 独立的 ext4 VHD；跨系统访问通过 9P 协议（大量 I/O 时较慢） |
| **内存占用** | 无 —— 没有 Hypervisor | 虚拟机预占内存（默认 50% 系统 RAM 或 8 GB） |
| **Windows 工具互操作** | Cygwin 与 `cmd`/PowerShell 工具共享同一进程空间和句柄 | 需要 `wsl.exe` 桥接或 Windows interop 方案 |
| **企业/受限机器** | Cygwin 是纯 Win32 DLL，即便 Hyper-V 被策略禁用也能运行 | 需要 Hyper-V / Virtual Machine Platform 功能，在很多企业环境中被禁 |
| **已有 Cygwin** | cygctl 为其提供可脚本化、可管道化的接口 | 额外引入一套你可能不需要的 Linux 环境 |

**选 WSL 的情况：** 需要真实的 Linux 内核（Docker、eBPF、内核模块）、完整 glibc 兼容性，或特定 Linux 发行版。

**选 Cygwin + cygctl 的情况：** 已有 Cygwin 安装；机器上 Hyper-V 不可用；需要在 Unix Shell 中直接管理 Windows 文件和 ACL；构建需要通过 `su`/`sudo` 以特定 Windows 用户运行的 CI/CD 流水线。

---

**cygctl 不是 shell alias，也不是 shim 垫片。** 简单的 `alias cyg='bash.exe'` 在涉及管道、退出码或用户切换时就会失效。cygctl 是一个专门构建的二进制工具，负责处理：

- **正确的 stdio 连接** — stdin/stdout/stderr 被正确绑定，管道和重定向按预期工作
- **退出码传递** — 子进程的退出码原样返回给调用方，保障脚本和 CI 环境的可靠性
- **进程生命周期管理** — 通过 Windows Job Object 枚举、查看并终止 Cygwin 进程
- **UAC 提权** — `sudo` 启动提权子进程并桥接 I/O，这是任何 alias 都无法实现的
- **用户切换** — `su` 调用 `CreateProcessWithLogonW`，以另一个 Windows 账户身份启动进程
- **WSL 互操作** — 在 Cygwin 与 WSL 之间进行路径格式转换和跨环境命令调度
- **包管理** — 以 Go 重写的 `apt-cyg`，具备完整的依赖解析能力

cygctl 是单一可执行文件，无依赖，直接放入 PATH 即可使用，专为 AI Agent、开发者脚本和 CI/CD 流水线设计。

## 快速安装

```powershell
# PowerShell（一行命令）
irm https://raw.githubusercontent.com/chen0430tw/cygctl/master/install.ps1 | iex
```

安装完成后重启终端即可使用 `cyg` 和 `apt` 命令。

> [!WARNING]
> **PowerShell 执行策略报错（常见坑）**
>
> 如果出现以下错误，说明 PowerShell 默认禁止执行本地脚本：
> ```
> 因為這個系統上已停用指令碼執行，所以無法載入 ...Microsoft.PowerShell_profile.ps1
> ```
> 安装脚本会自动将当前用户的执行策略设置为 `RemoteSigned`（允许本地脚本，仍阻止未签名的网络下载脚本）。如果你是手动安装或遇到此错误，请在 PowerShell 中执行：
> ```powershell
> Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned -Force
> ```
> 然后重新打开终端即可。

> [!NOTE]
> **Cygwin 装在非 C 盘？** 安装脚本会自动从注册表（Cygwin setup 写入的路径）探测安装位置，无需手动指定。如果自动探测失败，也可以显式传入路径：
> ```powershell
> irm https://raw.githubusercontent.com/chen0430tw/cygctl/master/install.ps1 | iex; install.ps1 -CygwinRoot D:\cygwin64
> ```
> 或下载脚本后运行：
> ```powershell
> .\install.ps1 -CygwinRoot D:\MyCygwin
> ```

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

> [!WARNING]
> **`apt update` 报权限错误？**
>
> 如果 Cygwin 是由某个特定用户（例如 `asus`）安装的，`C:\cygwin64` 的 ACL 可能只给了该用户写权限，Administrators 组不在列表中。以该组身份执行 `sudo apt update` 时就会失败。
>
> 解决方法 —— 在提权后的 PowerShell 或 CMD 中执行一次：
> ```powershell
> icacls "C:\cygwin64" /grant "Administrators:(OI)(CI)F" /T
> ```
> 这会递归地将完全控制权授予 Administrators 组。之后 `sudo apt update` 和 `sudo apt install` 即可正常工作。

```bash
apt update               # 更新包列表
apt search python        # 搜索包
apt install vim git      # 安装包
apt list --installed     # 列出已安装的包
apt show bash            # 显示包信息
apt depends vim          # 显示依赖关系
apt upgrade              # 升级包
apt remove vim           # 移除包
```

### Sudo（UAC 提权）

```bash
sudo netstat -an
sudo notepad C:\Windows\System32\drivers\etc\hosts
```

### Su（切换用户）

```bash
# 以其他 Windows 用户身份打开交互式登录 Shell
su alice

# 以其他用户身份执行单个命令
su alice whoami

# 通过 cyg 指定用户
cyg --user alice --cd "D:\Projects"
```

> **注意：** `su` 使用 `CreateProcessWithLogonW`，需要 Windows 的 Secondary Logon 服务（`seclogon`）处于运行状态。该服务在所有现代 Windows 版本上默认启用。

> [!WARNING]
> **空密码账户无法通过 `su` 登录。** Windows 安全策略（`LimitBlankPasswordUse`）限制空密码账户只能通过本地交互式登录（控制台）访问，`CreateProcessWithLogonW` 所使用的网络/服务登录方式会被拒绝。使用 `su` 前，请确保目标账户已设置密码。

### WSL 集成（cyg wsl）

`cyg wsl` 让你在 Cygwin 内管理和操作 WSL。

```bash
# 交互式启动默认 WSL 发行版
cyg wsl

# 列出所有 WSL 发行版（含状态与版本）
cyg wsl --list

# 转换路径格式（Windows / Cygwin / WSL）
cyg wsl --path "C:\Users\alice"
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

## 在 AI Agent 和脚本中使用

安装完成后，`cyg` 和 `apt` 在**非交互式 Shell** 中也能直接使用 —— 包括 `bash -c`、子进程、管道，以及各类 AI Agent 工具环境（Claude Code、Cursor、OpenClaw 等）。

### 在没有 WSL 的 Windows 上运行 OpenClaw

[OpenClaw](https://openclaw.ai) 是一个开源自主 AI agent，通过 bash 执行 shell 命令。官方 Windows 支持要求 WSL2。如果你的机器无法使用 WSL2（Hyper-V 被策略禁用、没有虚拟化支持等），cygctl 可以作为替代方案：

1. 正常安装 Cygwin 和 cygctl。
2. 将 OpenClaw 的 shell 指向 Git Bash（Git Bash 继承 `BASH_ENV`，因此 `cyg`/`apt` 已自动可用）。
3. OpenClaw 的 `system.run` 调用会落在一个已有 Cygwin 工具的 bash 进程中 —— 无需 WSL。

```bash
# 在 Windows + Cygwin 环境下，这些命令在 OpenClaw 的 shell 工具中均可直接使用
cyg ls -la /cygdrive/c/Users
apt install git curl wget
cyg python3 my_script.py
```

```bash
# 以下用法安装后均可直接使用，无需 -i 参数
bash -c 'cyg ls -la /tmp'
bash -c 'apt install vim'
echo 'cyg ls /' | bash
```

**原理：** Bash 只在交互式会话中加载 `.bashrc`。对于非交互式 Shell，Bash 会读取环境变量 `BASH_ENV` 并 source 其指向的文件。安装器会：

1. 将 `cyg`/`apt` 函数写入 `~/.bash_env`（不含任何交互式 guard）
2. 将 `BASH_ENV=%USERPROFILE%\.bash_env` 写入 Windows 用户环境变量

Git Bash 会继承 Windows 用户环境变量，因此每一个新的 bash 进程 —— 无论交互式还是非交互式 —— 都会自动加载这些 alias。

> [!NOTE]
> `BASH_ENV` 仅对**新启动**的进程生效。如果你的终端在安装前已经打开，请重新开一个终端窗口。

> [!WARNING]
> **Cygwin 交互式 Shell 的坑：** Cygwin 的 `$HOME` 是 `/home/<用户名>`（即 `C:\cygwin64\home\<用户名>\`），而 `.bash_env` 存放在 Windows 的 `%USERPROFILE%`（`C:\Users\<用户名>\`）。两者是不同目录，因此 Cygwin 的 `.bashrc` 不能直接写 `source $HOME/.bash_env`，必须用 `cygpath` 做路径转换：
> ```bash
> [ -f "$(cygpath -u "$USERPROFILE")/.bash_env" ] && source "$(cygpath -u "$USERPROFILE")/.bash_env"
> ```
> 安装脚本已处理此问题。如果你是手动安装，需自行在 Cygwin 的 `~/.bashrc` 里写上这行。

**手动配置（未使用安装脚本时）：**

```powershell
# PowerShell — 手动创建 ~/.bash_env 并配置 BASH_ENV
$utf8NoBom = New-Object System.Text.UTF8Encoding $false
$bashEnvContent = @'
cyg()    { MSYS_NO_PATHCONV=1 cygctl.exe  "$@"; }
apt()    { MSYS_NO_PATHCONV=1 apt-cyg.exe "$@"; }
'@
[System.IO.File]::WriteAllText("$env:USERPROFILE\.bash_env", $bashEnvContent, $utf8NoBom)
[Environment]::SetEnvironmentVariable("BASH_ENV", "$env:USERPROFILE\.bash_env", "User")
```

## install.ps1

安装脚本会自动配置：

1. **下载** - 从 GitHub Releases 获取二进制文件
2. **PATH** - 将 `C:\cygwin64\bin` 加入用户 PATH
3. **PowerShell** - 创建 `cyg` 和 `apt` 函数
4. **CMD** - 使用 AutoRun 创建 doskey 宏
5. **Shell 别名** - 将 `cyg`/`apt` 函数写入 `~/.bash_env`，设置 `BASH_ENV` Windows 环境变量，并在 `~/.bashrc` 中添加 source 语句（交互式和非交互式 Shell 均可用）
6. **Cygwin** - 在 Cygwin 的 `~/.bashrc` 中添加 source 语句

## 构建

需要 Go 1.21 及以上版本

```bash
make all      # 构建全部
make install  # 安装至 Cygwin bin
make clean    # 清理
```

## 许可证

MIT
