---
name: git-commit-release
description: "为 Git 变更设计和执行中文提交、版本 tag 与发布流程；适用于生成完善提交信息、整理提交范围、创建发布说明、打 tag 和发布 GitHub Release，不替代代码审查或部署。"
metadata:
  short-description: "用中文规范提交、打 tag 并发布版本"
---

# Git 提交与发布

为当前项目完成可追溯、可审阅的 Git 提交流程。提交主题和正文使用中文；提交类型保持 Conventional Commits 兼容的英文标识，以便现有 CI、release-drafter 或 commitlint 继续工作。

## 模式

先判断用户要哪种结果，不要默认扩大范围：

- **提交信息**：只分析差异并生成一个或多个候选中文 commit message，不修改 Git 状态。
- **提交**：整理明确的变更范围，运行必要检查，展示即将提交的路径和完整 message；用户已明确要求提交时才执行 `git commit`。
- **打 tag**：在提交已确定、版本号明确且检查通过后创建 annotated tag。没有版本号时先根据现有 tag 和变更类型给出建议并询问，不猜测正式版本。
- **发布**：准备中文 release notes，并在用户明确要求平台发布时使用仓库已有工具（优先 `gh release create`）创建 Release。提交、推送、tag 和 Release 是不同动作，不要因为用户要求其中一个就自动执行其他动作。
- **完整发布**：按“检查 -> 提交 -> 推送 -> annotated tag -> 推送 tag -> 创建 Release”的顺序执行；每一步失败都停止，不跳过失败继续发布。

## 仓库识别

1. 从当前目录执行 `git rev-parse --show-toplevel`，确定实际 Git 根目录；当前目录不是 Git 根时，继续向下检查明确的嵌套仓库，但不要把多个仓库混为一个提交。
2. 读取最近的 `AGENTS.md`、贡献指南、commitlint 配置、发布工作流、changeset/release 配置和远端信息。仓库约定优先于本技能的通用示例。
3. 执行并记录：

   ```bash
   git status --short --branch
   git diff --stat
   git diff --cached --stat
   git log -5 --oneline --decorate
   git tag --sort=-v:refname | head -20
   git remote -v
   ```

4. 如果工作树已经有用户改动，先区分既有改动、当前任务改动和生成文件。不得用 `git reset --hard`、`git checkout --`、`git clean` 或其他破坏性命令替用户清理现场。
5. 当前仓库没有 Git 元数据时，只能生成提交建议和发布模板，并明确无法执行 commit、tag 或 release。

## 提交流程

### 1. 确定提交边界

- 阅读完整 diff，不只看文件名；必要时用 `git diff -- path` 和 `git diff --cached` 分别确认工作区与暂存区。
- 一个 commit 只表达一个逻辑变化。实现、测试、文档和配置可以同属一个逻辑变化；不相关的重构、格式化、生成物和用户既有改动不要混入。
- 不使用 `git add -A` 作为默认动作。只暂存确认属于本次提交的路径；无法可靠区分时，停止并请求用户指定范围。
- 检查敏感信息：密码、Token、私钥、生产配置、数据库 DSN、个人数据和大体积构建产物不得提交。发现疑似凭据时先阻断提交并说明路径。
- 检查 `git diff --check`；对代码变更按仓库约定运行格式化、测试、静态检查和构建。不要把未运行的命令写成“已通过”。

### 2. 生成中文提交信息

默认格式：

```text
<type>(<scope>): <中文主题>

背景：
- 为什么需要这次变更。

变更：
- 做了什么，以及关键行为如何变化。

影响：
- 影响的模块、兼容性、迁移、配置或运维注意事项。

验证：
- `实际执行的命令`
- `实际执行的命令`
```

规则：

- `type` 使用 `feat`、`fix`、`refactor`、`perf`、`docs`、`test`、`build`、`ci`、`chore` 或 `revert`；只选择最能表达主要行为的一个。
- `scope` 使用仓库已有模块名，例如 `system`、`auth`、`config`；范围不确定时可以省略，不要编造。
- 主题使用中文、动词开头、简洁明确，不以句号结尾，不把测试结果塞进主题。
- 正文优先说明动机、行为、影响和验证；不要只重复 diff，也不要写空泛的“优化代码”。
- 有数据库迁移、配置变化、安全影响、兼容性变化、回滚方式或发布注意事项时必须写入正文。
- 破坏性变更同时使用 `!`，并在 footer 写 `BREAKING CHANGE: 中文说明`。
- 多个互不依赖的逻辑变化应拆成多个 commit，并为每个 commit 分别生成 message。
- 提交正文不记录凭据、Token、完整请求载荷或生产数据。

详细模板和示例见 [references/chinese-commit-and-release.md](references/chinese-commit-and-release.md)。

### 3. 执行提交

在执行前输出短预览：目标仓库、当前分支、将提交的路径、检查结果和完整 commit message。确认范围没有包含既有无关改动后再执行：

```bash
git add -- <明确路径...>
git diff --cached --check
git diff --cached --stat
git diff --cached
git commit -F <临时提交说明文件>
```

临时说明文件应在提交完成后删除，不要把它留在工作树。若仓库已有 commit-msg hook、commitlint 或签名要求，遵循它们，不绕过 hook；除非用户明确要求，不使用 `--no-verify` 或 `--no-gpg-sign`。

提交完成后核对 `git status --short --branch`、`git log -1 --format=fuller` 和提交包含的文件。报告 commit SHA，不把未推送的 commit 描述成已发布。

## 版本、tag 与发布

### 版本判断

- 优先遵循仓库现有版本来源：`package.json`、`go.mod`、changeset、发布脚本、已有 tag 或 CI 配置。
- 默认采用 SemVer：修复为 patch，向后兼容功能为 minor，破坏性变更为 major；预发布版本使用 `-rc.1`、`-beta.1` 等明确后缀。
- 现有 tag 格式、前缀和排序方式优先于通用建议。常见格式是 `vX.Y.Z`，但不得擅自把 `X.Y.Z` 改成 `vX.Y.Z` 或反过来。
- tag 已存在时停止，不覆盖、移动或强制更新 tag；需要修订版本时创建新的版本号。

### 发布前检查

至少确认：

- 工作树没有未预期的未提交改动，或用户明确允许带着指定改动发布。
- 当前分支、上游分支和远端目标正确；没有把 feature 分支误发布成默认分支版本。
- 提交已包含预期文件，版本来源已同步，必要的迁移、配置和文档已更新。
- `git diff --check`、仓库要求的测试、静态检查、构建和发布前检查均有实际结果。
- 发布说明已按新增、修复、变更、破坏性变更、迁移/运维和验证分类，并避免泄漏敏感信息。

### 创建 tag

使用 annotated tag，说明应能独立解释版本内容：

```bash
git tag -a vX.Y.Z -F <临时 tag 说明文件> <目标 commit>
git show --stat --decorate --oneline <tag>
```

不要默认执行 `git push`。只有用户明确要求推送时才执行：

```bash
git push <remote> <branch>
git push <remote> <tag>
```

推送失败时保留本地结果并报告准确状态，不重试造成重复发布，也不使用 `--force`，除非用户明确指定且风险已说明。

### 创建平台 Release

- 优先检查仓库是否已有 `.github/workflows/release*.yml`、release-drafter、changeset 或其他发布机制，避免绕过既有自动化。
- GitHub 仓库且 `gh auth status` 可用时，可使用：

  ```bash
  gh release create <tag> --title "<中文标题>" --notes-file <发布说明文件>
  ```

- 没有认证、网络、发布工具或明确平台时，只生成发布说明和可执行命令，不声称 Release 已创建。
- Release 创建成功后核对 URL、tag、目标 commit 和发布状态；如果 tag 已推送但 Release 失败，明确记录“tag 已发布、Release 未完成”，不要删除 tag 伪造回滚。

## 输出格式

### 只生成提交信息

输出：变更摘要、建议 commit message、拆分建议、待确认项；不声称已提交。

### 已提交

输出：仓库/分支、commit SHA、实际提交路径、检查命令与结果、剩余未提交改动。若没有 push，明确写“仅本地提交”。

### 已发布

输出：版本号、tag、目标 commit、是否推送、Release URL、发布说明摘要、检查结果和失败或剩余风险。区分“commit 已创建”“tag 已创建”“tag 已推送”“Release 已创建”四种状态。

## 安全边界

- 不替用户决定版本号、发布分支、远端、是否推送或是否创建正式 Release；缺失信息会改变结果时先询问。
- 不重写已有公共历史，不删除 tag，不强推，不清理用户改动。
- 不因提交失败、网络失败或 hook 失败而绕过检查；先修复原因或报告阻塞点。
- 变更涉及数据库迁移、配置、凭据、安全策略或不可逆数据操作时，在 commit 和 release notes 中明确写出影响。
