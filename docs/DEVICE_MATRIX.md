# 被调试方环境对照表（开发标准）

以真实设备实测为准。开发目标：产物在两台设备上都不用改代码就能跑。

## 实测设备

| 项 | corot（开发机） | PHJ110（第二目标） |
|---|---|---|
| 型号 | Redmi 23078RKD5C | OPPO PHJ110 |
| Android | 15 (SDK 35) | 13 (SDK 33) |
| 内核 | 5.15.167-android13 | 4.19.191+ |
| ABI | arm64-v8a | arm64-v8a |
| 页大小 | 4096 | 4096 |
| ROM | crDroid（root, Magisk） | OPPO 量产（locked, 非root） |

## 关键约束（两台都实测）

| 约束 | corot | PHJ110 | 对产物的影响 |
|---|---|---|---|
| `/system/bin/bash` | 无 | 无 | 不能假定 bash；shell 工具候选列表已探测 |
| `/system_ext/bin/bash` | **有**（crDroid 附带） | 无 | 不能依赖它，探测到才用 |
| `/system/bin/sh` 方言 | mksh | mksh | `[[ ]]`/数组可用；`${v,,}` bad substitution |
| `git` 二进制 | 无 | 无 | cardgit 走 go-git 进程内实现 |
| `rg`/`curl` | 无 | 无 | 辅助工具不可依赖外部二进制 |
| `/data/local/tmp` | shell 用户所有，app 不可写 | 同左 | 不能当临时目录 |
| `/sdcard` 挂载 | sdcardfs/FUSE `noexec` | 同左 | 该分区不可执行、不可当工作目录写入脚本 |
| app 私有目录 execve | **可用**（crDroid 未启用 W^X） | **可用**（实测） | 不可依赖：量产 Android 14/15 普遍禁止，产物仍须假设被禁 |
| TLS cacerts | 145 | 125 | Go x509 自动读，无障碍 |
| DNS | netd socket（net.dns* 属性为空） | 同左 | Go cgo resolver 走 Bionic；netgo 走 /etc/resolv.conf（Android 无）→ 构建必须 CGO_ENABLED=1 |
| SELinux | Enforcing，app 域 untrusted_app | 同左 | 运行时假设最严域 |
| Play 16KB 页 | 设备 4096，但 API 35+ 上架要求 .so 16KB 对齐 | 设备 4096 | jni/build.sh 已加 `-Wl,-z,max-page-size=16384` |

## 与常见设备的差异（corot 独有，不可当开发标准）

| corot 现状 | 常见设备 | 产物策略 |
|---|---|---|
| droidspaces 容器 + Magisk root | 无 root、无容器 | 产物零 root 依赖 |
| droidspacesd 域 permissive=1（实测放行容器 execve） | enforcing | 假设 enforcing |
| app home execve 可用（crDroid 未启用 W^X） | Android 14/15 默认禁止 | shell 执行走 `/system/bin/sh`（系统路径）；不往 app home 写可执行文件再执行 |

## 构建与验证通道

- **usb 调试**（开发标准）：corot 作 USB host 直连 PHJ110。容器内 `mknod /dev/bus/usb/001/004 c 189 3` 补节点后 `adb` 可用（OZAMXWSSFQBIN7DU）。
- **folkbridge**（部署/测量通道）：corot root，`fb exec/push/pull`。
- APK 安装到 corot：`fb push` + `fb exec -- pm install -r`。

## 产物红线（从此表推出）

1. 不依赖 root、不依赖容器。
2. 不依赖 `/system_ext/bin/bash`（探测到才用，未探测到回退 `/system/bin/sh` mksh）。
3. 不依赖任何外部二进制（git/rg/curl 均无）；辅助能力进程内实现。
4. 不写 `/data/local/tmp`、不把 `/sdcard` 当可执行工作区。
5. app 私有目录可执行性视为不可用（量产机默认禁止）。
6. JNI .so 必须 16KB 对齐。
7. CGO_ENABLED=1（DNS 依赖 Bionic resolver）。

## mksh 版本与方言基线

| 设备 | KSH_VERSION |
|---|---|
| corot (A15) | R59 2020/10/31 Android |
| PHJ110 (A13) | R59 2020/05/16 Android |

两台同为 R59，方言一致。实测可用：`[[ ]]`、数组（含 `${a[1]}`）、`read -ra`、herestring。
实测不可用：`${v,,}`（bad substitution）、`sh --version`。

## DNS 决定性实测（PHJ110，2026-10-16）

同一探针程序，纯 Go 与 cgo 两种构建：

| 构建 | 结果 |
|---|---|
| `CGO_ENABLED=0`（netgo） | `dns: 0 lookup ... on [::1]:53: read: connection refused` |
| `CGO_ENABLED=1`（cgo resolver → Bionic getaddrinfo → netd） | `dns: 1 <nil>` |

netgo 的失败路径：Android 无 `/etc/resolv.conf` → Go 读不到 → 回退 `127.0.0.1:53`/`[::1]:53` → 没有本地递归解析器 → 拒绝。

**结论：任何要在 Android 上发网络请求的构建都必须 `CGO_ENABLED=1`。**
桌面构建同样要显式，否则 netgo 在无 `/etc/resolv.conf` 的环境（含本容器与部分最小发行版）同错。

## 内嵌 Node 运行时（2026-10-16 实测闭环）

| 项 | 结果 |
|---|---|
| 来源 | Termux 官方 deb（nodejs 26.4.0 + 8 个依赖包解包），Bionic 编译 |
| 体积 | node 47MB + 35 个 .so + openssl.cnf → APK 41.7MB |
| 打包 | node 伪装 `libnodebin.so` 进 jniLibs；libs+cnf 打成 assets tgz（aapt2 link `-A`） |
| 释放 | NodeRuntime.java：nativeLibraryDir 复制 + cacheDir 暂存后 tar（toybox tar 不认 stdin gz） |
| 已踩坑 | tgz 内带 lib/ 前缀（解到 node 根）；OPENSSL_CONF 必须指向 bundle 内 etc/tls/openssl.cnf |
| 验证 | corot app uid 下 node -e 输出 `final-ok 5`；npm CLI 11.21.1 真装 dsh-free-search；插件动态加载 + Bing 搜索真实返回 |
| peer 链 | settings→cordis→scope→llm→timeout→session（循环补装到 import 满足） |
