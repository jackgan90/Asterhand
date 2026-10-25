# ADR-003：固定安装入口与版本解析

- 状态：accepted
- 日期：2026-10-25
- 替代：[ADR-002](002-release-and-install-boundary.md) 中安装脚本默认绑定其所属 Release 的版本选择方式；保留安装与配置分离、依赖锁定和草稿校验流程。

## 决策

Windows 安装入口是仓库根目录 install.ps1，通过默认分支的固定 raw URL 下载。默认使用 GitHub Releases Latest 接口选择稳定版，不包含 draft/prerelease。Version 指定应用版本，ReleaseTag 指定精确标签；无稳定版时给出明确错误和指定预发布版本的方法。

入口脚本可更新，单次安装目标必须固定。Latest 只查询一次，解析出的标签用于清单和平台包下载，清单与程序版本、包摘要及大小继续强校验。所有运行依赖仍随所选 Release 锁定，不查询依赖最新版。

本地包依据本地清单安装，不查询 Latest。Release 附件可保留该脚本的副本，但构建仅设置仓库，不改变默认版本选择语义；安装某个指定 Release 时传入其标签。

Linux/macOS 将来使用各自受版本控制的固定脚本入口，采用相同的默认稳定版、显式版本和标签固定规则，平台专属依赖安装保持独立。当前不启用这两个平台。

## 依据与影响

[Helm 官方安装脚本](https://helm.sh/docs/intro/install/) 采用仓库固定脚本获取最新版的模式。[GitHub Latest 接口](https://docs.github.com/en/rest/releases/releases#get-the-latest-release) 提供稳定 Release 选择语义。普通安装用户无需先浏览 Release 附件；自动化部署仍可通过显式版本获得可重复目标。

固定入口发布依赖源码推送，二进制发布仍由 Release 流程单独完成。默认分支脚本需保持对受支持清单版本的兼容，遇到未知 schema 明确拒绝。脚本和清单通过项目官方 HTTPS 来源获取；摘要用于完整性校验，不宣称替代发布签名。
