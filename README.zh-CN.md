[English](./README.md) | 简体中文

# cygctl

> [!IMPORTANT]
> **cygctl 不會安裝、下載或配置 Cygwin 或 WSL。**
> 它是一個控制工具，用於在**已安裝的 Cygwin 環境**中執行命令，提供類似 WSL 的介面。
> 如需安裝 Cygwin，請訪問 [cygwin.com](https://www.cygwin.com)。如需安裝 WSL，請參閱 [Microsoft 文件](https://learn.microsoft.com/windows/wsl/install)。

一個面向 AI Agent 和開發者的 Cygwin WSL 風格命令列工具。

## 功能特性

- **單一可執行檔** - 無依賴，直接放入 PATH 即可使用
- **WSL 風格介面** - WSL 用戶熟悉的語法
- **AI Agent 友好** - 簡單、可預期的命令結構
- **完整的標準輸入/輸出支援** - 正確的管道處理
- **退出碼傳遞** - 腳本使用時傳回正確退出碼
- **套件管理** - 以 Go 重寫的 apt-cyg
- **UAC 提權** - sudo 用於 Windows 管理員任務
- **使用者切換** - su 用於切換 Windows 使用者帳戶

## 快速安裝

```powershell
# PowerShell（一行指令）
irm https://raw.githubusercontent.com/chen0430tw/cygctl/master/install.ps1 | iex
```

安裝完成後重啟終端即可使用 `cyg` 和 `apt` 命令。

## 手動安裝

```powershell
# 下載二進位檔到 Cygwin bin 目錄
$bin = "C:\cygwin64\bin"
Invoke-WebRequest -Uri "https://github.com/chen0430tw/cygctl/releases/latest/download/cygctl.exe" -OutFile "$bin\cygctl.exe"
Invoke-WebRequest -Uri "https://github.com/chen0430tw/cygctl/releases/latest/download/apt-cyg.exe" -OutFile "$bin\apt-cyg.exe"
Invoke-WebRequest -Uri "https://github.com/chen0430tw/cygctl/releases/latest/download/sudo.exe" -OutFile "$bin\sudo.exe"
Invoke-WebRequest -Uri "https://github.com/chen0430tw/cygctl/releases/latest/download/su.exe" -OutFile "$bin\su.exe"

# 加入 PATH
[Environment]::SetEnvironmentVariable("PATH", "$bin;" + [Environment]::GetEnvironmentVariable("PATH", "User"), "User")
```

## 組成元件

| 檔案 | 說明 |
|------|------|
| `cygctl.exe` | 主要 CLI 工具 |
| `apt-cyg.exe` | 套件管理器 |
| `sudo.exe` | UAC 提權 |
| `su.exe` | 切換 Windows 使用者（透過 `CreateProcessWithLogonW`） |

## 使用方式

### WSL 命令對照

如果你熟悉 WSL，以下是 WSL 命令與 `cyg` 的對應關係：

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
# 互動式 Shell
cyg

# 執行命令
cyg --exec "ls -la /cygdrive/c"
cyg -e "echo hello"

# 切換目錄後執行
cyg --cd "D:\Projects" --exec "pwd"

# 直接傳入命令（類似 wsl）
cyg ls -la /tmp
```

### 套件管理（apt-cyg）

```bash
# 更新套件清單
apt update

# 搜尋套件
apt search python

# 安裝套件
apt install vim git

# 列出已安裝套件
apt list --installed

# 顯示套件資訊
apt show bash

# 顯示依賴關係
apt depends vim

# 升級套件
apt upgrade

# 移除套件
apt remove vim
```

### Sudo（UAC 提權）

```bash
# 以管理員權限執行命令
sudo netstat -an

# 編輯受保護的檔案
sudo notepad C:\Windows\System32\drivers\etc\hosts
```

### Su（切換使用者）

```bash
# 以其他 Windows 使用者身份開啟互動式登入 Shell
cyg --user alice
su alice

# 以其他使用者身份執行單一命令
cyg --user alice --exec "whoami"
su alice whoami

# 切換目錄後以其他使用者身份開啟 Shell
cyg --user alice --cd "D:\Projects"
```

> **注意：** `su` 使用 `CreateProcessWithLogonW`，需要 Windows 的 Secondary Logon 服務（`seclogon`）處於執行狀態。該服務在所有現代 Windows 版本上預設為啟用。

### WSL 整合（cyg wsl）

`cyg wsl` 讓你在 Cygwin 內管理和操作 WSL。

```bash
# 互動式啟動預設 WSL 發行版
cyg wsl

# 列出所有 WSL 發行版（含狀態與版本）
cyg wsl --list

# 轉換路徑格式（Windows / Cygwin / WSL）
cyg wsl --path "C:\Users\alice"
cyg wsl --path /cygdrive/c/Users/alice
cyg wsl --path /mnt/c/Users/alice
# 輸出：windows=C:\Users\alice
#       cygwin=/cygdrive/c/Users/alice
#       wsl=/mnt/c/Users/alice

# 在預設 WSL 發行版中執行命令
cyg wsl --exec -- ls -la /tmp

# 在指定 WSL 發行版中執行命令
cyg wsl --exec Ubuntu -- whoami

# 關閉所有 WSL2 虛擬機器
cyg wsl --shutdown
```

| 選項 | 縮寫 | 說明 |
|------|------|------|
| `--list` | `-l` | 列出發行版（含名稱、狀態、WSL 版本） |
| `--path <path>` | `-p` | 轉換路徑為 Windows / Cygwin / WSL 格式 |
| `--exec [distro] -- <cmd>` | `-e` | 在發行版中執行命令（省略則使用預設） |
| `--shutdown` | | 關閉所有 WSL2 虛擬機器 |

### 狀態與管理

```bash
cyg --status    # 顯示 Cygwin 狀態
cyg --shutdown  # 終止所有 Cygwin 進程
cyg --version   # 顯示版本
cyg --help      # 顯示說明
```

## apt-cyg 命令一覽

| 命令 | 說明 |
|------|------|
| `update` | 下載最新套件清單 |
| `install <pkg...>` | 安裝套件（含依賴） |
| `remove <pkg...>` | 移除套件 |
| `search <pattern>` | 搜尋套件 |
| `list [--installed]` | 列出所有或已安裝套件 |
| `show <package>` | 顯示套件資訊 |
| `depends <package>` | 顯示依賴關係 |
| `rdepends <package>` | 顯示反向依賴 |
| `upgrade [pkg...]` | 升級套件 |
| `download <pkg...>` | 僅下載不安裝 |
| `autoremove` | 尋找未使用的依賴 |
| `clean` | 清除套件快取 |
| `mirror [url]` | 設定或顯示鏡像站 |

## install.ps1

安裝腳本會自動配置：

1. **下載** - 從 GitHub Releases 獲取二進位檔
2. **PATH** - 將 `C:\cygwin64\bin` 加入使用者 PATH
3. **PowerShell** - 建立 `cyg` 和 `apt` 函式
4. **CMD** - 使用 AutoRun 建立 doskey 巨集
5. **Git Bash** - 在 `.bashrc` 中加入別名
6. **Cygwin** - 在 `.bashrc` 中加入別名

## 建置

需要 Go 1.21 以上版本

```bash
# 建置全部
make all

# 安裝至 Cygwin bin
make install

# 清理
make clean
```

## 為什麼需要 cygctl？

現有的 Cygwin 包裝工具（如 mintty）是終端機模擬器，而非命令列工具。cygctl 填補了這個空缺，為 Cygwin 提供類似 `wsl` 的介面，讓以下使用場景更加便利：

- AI Agent 執行 Cygwin 命令
- 開發者撰寫 Cygwin 操作腳本
- CI/CD 管線與 Cygwin 互動

## 授權條款

MIT
