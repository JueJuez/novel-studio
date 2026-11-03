# 第三方许可声明（THIRD_PARTY_NOTICES）

本仓库为 Apache-2.0 许可（继承上游 [Xiaoyangy/novel-studio](https://github.com/Xiaoyangy/novel-studio)）。以下列出本派生版实际引入的第三方代码与许可。

## Show Me The Story（MIT）

- **仓库**：https://github.com/Nigh/show-me-the-story @ `f790187373f23dbaaffa0838197e972a394960d3`
- **许可**：MIT License（Copyright (c) 2026 xianii）
- **引入内容与位置**：
  - `internal/agents/arc_rehearsal_input.go` 中的整弧预演输入构建——参考其章节生成与知识检索交互设计。
  - `internal/shortstory/retrieval.go` 中的轻量 BM25 + CJK bigram 检索——适配自 `internal/story/knowledge_retrieval.go`，保留算法逻辑与整条预算策略，语料改为短篇扁平知识条目。
  - `internal/authoring/blocks.go` 中的块级编辑合同——参考其块寻址与按块 revise 端点设计。
- **完整移植记录**：`docs/dual-pipeline-porting-audit.md`（R1/R2/R3 项）。

MIT 许可证原文：

```
MIT License

Copyright (c) 2026 xianii

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## Pagekin / AI-Novel-Writer（GPL v3）

- **仓库**：https://github.com/EthanYoQ/AI-Novel-Writer @ `59153ceb1174b20339e205b801f4e694e5ad5507`
- **许可**：GNU General Public License v3
- **处理方式**：仅做行为级交互参考（修稿差异、逐段接受、版本回退），**未复制任何代码或资产**。全部交互在 `internal/authoring/` 与 `internal/shortstory/` 独立实现。详见移植审计 P1 记录。

## 第三方 Go 依赖

- `github.com/voocel/agentcore`（Apache-2.0）— 角色 Agent 框架
- `github.com/voocel/litellm`（Apache-2.0，vendored 于 `third_party/litellm/`）— 多 provider LLM 客户端
- `golang.org/x/text`、`golang.org/x/image`（BSD）— 文本与图像处理
