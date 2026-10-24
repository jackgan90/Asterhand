# F001：公开源码仓库与首次提交

日期：2026-10-24。范围：仓库初始化和源码发布，产品功能仍未实现。

## 授权与目标

用户明确要求将 F:\Asterhand 初始化为 Git 仓库，在自己的 GitHub 账号创建远端并 push，随后确认公开可见，并要求包含其新增 LICENSE。

关联 R26、DEV-09；相关文档：[开发需求](../../requirements.md)、[开发设计](../../design.md)、[全局需求](../../../overview/requirements.md)。

## 范围

- 创建 jackgan90/Asterhand 公开仓库，默认分支 main，origin 使用 SSH 地址。
- 保留用户提供的 MIT LICENSE 原文，包含于首次提交。
- 同步 README、贡献说明、安装、打包及开发文档中的许可证和仓库状态。
- 暂存并审查完整文档骨架，创建首次提交，push 并核对本地与远端 main。
- 不编写功能、不安装依赖、不创建产品二进制 Release、不强制覆盖远端。

## 验证

- 文档链接、需求追溯、暂存范围和 git diff --cached --check。
- 检查已知凭据格式、私钥头等误提交迹象，不将此视为完整安全审计。
- LICENSE SHA-256 应保持 75ec6b3137ff60816d7f405aac0593cb4b13bb9f4b5b6a1dff5bb4c4c09c5973。
- commit 和 push 的最终状态以 Git refs 及远端仓库为准，初始化提交不会包含自身 SHA。

## 规模与后续

仅文档和 Git 元数据变化，未触发超限文件拆分。后续仍需用户授权进入 M0；本次源码公开不代表安装器可用或产品已验收。
