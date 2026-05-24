# 第 14 章　长期记忆系统

## 14.1　这一章要解决什么问题

GoClaw 的记忆系统，不是简单地把历史对话做摘要，而是要解决两个更具体的问题：

- 让 Agent 在**跨会话**场景下持续了解用户
- 让这些了解能在**后续推理时重新注入**，而不是只停留在存储层

它记住的内容分成三类：

- 用户画像：工作背景、个人背景、当前关注点
- 时间线：近期动态、较早背景、长期背景
- 离散事实：可枚举、可筛选、可人工编辑的 facts

从实现上看，它是一套：

- 由 `MemoryMiddleware` 驱动的读写入口
- 由 `JSONFileStore` 落盘的存储层
- 由 `UpdateQueue` 做异步防抖更新
- 由 LLM / 规则提取器负责生成记忆更新
- 由 Gateway API 暴露人工查看与编辑能力

---

## 14.2　核心文件地图

建议先建立模块地图，再读细节。

| 职责 | 文件 |
|---|---|
| Middleware 接线 | `internal/middleware/builder/builder.go` |
| 记忆主流程 | `internal/middleware/memory/memory.go` |
| 消息过滤、纠错检测、规则提取 | `internal/middleware/memory/llm.go` |
| 完整记忆更新器 | `internal/middleware/memory/updater.go` |
| Fact-only LLM 提取器 | `internal/middleware/memory/llm_extractor.go` |
| 存储结构与文件存储 | `internal/middleware/memory/facts.go` |
| 注入选择与格式化 | `internal/middleware/memory/query.go` |
| 提取提示词 | `internal/middleware/memory/prompt.go` |
| 配置结构 | `internal/config/config.go` |
| Memory API | `pkg/gateway/handlers/memory.go` |
| 集成测试 | `internal/middleware/memory/integration_test.go` |

---

## 14.3　整体架构

可以把长期记忆拆成两条链路来理解：

### 1）读链路：注入给模型

```text
MemoryStore.Load()
    ↓
selectFactsForInjection()
    ↓
formatMemoryForInjection()
    ↓
MemoryMiddleware.BeforeModel()
    ↓
把 <memory> 块注入 system prompt
```

### 2）写链路：从对话中更新记忆

```text
一次 agent run 结束
    ↓
MemoryMiddleware.AfterAgent()
    ↓
filterMessagesForMemory()
    ↓
detectCorrection()
    ↓
UpdateQueue.Add(threadID)
    ↓
防抖计时结束后 process()
    ↓
extractAndSave()
    ↓
LLM / 规则提取
    ↓
ApplyUpdates() / AddFact()
    ↓
JSONFileStore.Save()
```

它本质上是一个 **read-before-model + async-write-after-run** 的设计。

---

## 14.4　数据模型

记忆总结构定义在 `internal/middleware/memory/facts.go`。

### 1）Memory 顶层结构

```text
Memory
├── Version
├── LastUpdated
├── User
│   ├── WorkContext
│   ├── PersonalContext
│   └── TopOfMind
├── History
│   ├── RecentMonths
│   ├── EarlierContext
│   └── LongTermBackground
└── Facts[]
```

### 2）为什么不是只存 Facts

这个项目的设计不是纯 fact memory，而是 **summary memory + fact memory** 的混合方案。

- `User`：描述用户画像与当前关注点
- `History`：描述时间维度上的连续背景
- `Facts`：描述离散、稳定、可筛选的事实点

这样设计的好处是：

- facts 适合精确、结构化、可过滤
- summary 适合表达持续变化的语义状态
- 两者配合，能避免记忆完全碎片化

### 3）Fact 的结构

```text
MemoryFact
├── ID
├── Content
├── Category
├── Confidence
├── CreatedAt
├── Source
└── SourceError(optional)
```

其中 `Category` 定义在 `internal/middleware/memory/fact.go`，支持：

- `preference`
- `knowledge`
- `context`
- `behavior`
- `goal`
- `correction`

`correction` 是这个系统里比较重要的一类，它专门用于保存“用户纠正了 agent”的信息。

---

## 14.5　存储机制

### 1）默认存储位置

默认路径常量定义在：

- `internal/middleware/memory/memory.go`

默认值是：

```text
memory.json
```

如果配置里显式指定 `memory.storage_path`，则使用配置值。

在当前仓库的 `config.yaml` 中，默认示例也是：

```yaml
memory:
  storage_path: memory.json
```

### 2）存储实现

文件：`internal/middleware/memory/facts.go`

核心实现是 `JSONFileStore`，职责包括：

- `Load()`：读取 memory 文件
- `Save()`：原子写回 memory 文件
- `AddFact()`：向 facts 里追加新事实
- `Deduplicate()`：去除冗余事实

### 3）为什么用文件存储

它没有依赖数据库，而是直接用 JSON 文件，优点是：

- 部署简单
- 调试友好
- 便于导入导出
- 与单机 agent runtime 的定位匹配

### 4）原子写入

写入逻辑是：

```text
marshal JSON
    ↓
写入 memory.json.tmp
    ↓
rename 替换正式文件
```

这样可避免半写入导致的损坏。

### 5）mtime 缓存

`Load()` 带有基于文件修改时间的缓存策略：

- 文件没变化：直接返回内存缓存
- 文件变化了：重新读取并刷新缓存

这可以减少频繁磁盘读取。

---

## 14.6　记忆注入：BeforeModel 阶段做什么

入口函数：

- `internal/middleware/memory/memory.go` `BeforeModel()`

### 注入流程

```text
BeforeModel()
    ↓
store.Load()
    ↓
检查 injection_enabled
    ↓
selectFactsForInjection()
    ↓
formatMemoryForInjection()
    ↓
插入或前置到 system message
```

### 关键点 1：不是只注入 facts

注入块会包含三部分：

- User Context
- History
- Facts

格式化逻辑在 `internal/middleware/memory/query.go`。

### 关键点 2：当前实现是“最近优先”，不是“按置信度优先”

`selectFactsForInjection()` 的行为是：

1. 先截取最近的 `maxFacts`
2. 再按 token 预算从后向前保留最新 facts

也就是说当前实现更偏向：

- **近期性优先**
- 然后受 `max_injection_tokens` 限制

而不是先对 facts 按 confidence 排序再注入。

### 关键点 3：有 token 预算

`estimateTokenCount()` 会粗略估算文本 token 数：

- ASCII 字符：约 4 chars / token
- 非 ASCII 字符：约 1.5 chars / token

这是一个轻量但实用的近似算法。

### 注入结果示意

```text
<memory>
User Context:
- Work: ...
- Personal: ...
- Current Focus: ...

History:
- Recent: ...
- Earlier: ...
- Long-term: ...

Facts:
- ...
- ...
</memory>
```

---

## 14.7　记忆更新：为什么在 AfterAgent 而不是 AfterModel

入口函数：

- `internal/middleware/memory/memory.go` `AfterAgent()`

这是这套设计里最关键的点之一。

### 原因

如果在 `AfterModel()` 就更新记忆，会遇到这些问题：

- 一次 agent run 可能有多轮模型调用
- 中间 assistant 消息可能只是工具调用前的过渡消息
- 工具结果和中间状态会污染长期记忆

因此当前实现选择：

- `AfterModel()` 基本不做事
- `AfterAgent()` 在**整次运行结束后**统一更新

这样能保证记忆只基于一次完整 run 的有效结果来更新。

---

## 14.8　更新前的消息过滤

过滤逻辑在：

- `internal/middleware/memory/llm.go` `filterMessagesForMemory()`

### 会保留的内容

- `human`
- 最终的 `assistant`

### 会丢弃的内容

- `tool`
- 带 `tool_calls` 的 assistant 中间步骤

也就是说，记忆更新只关注：

- 用户说了什么
- 最终助手回复了什么

而不是关注中间执行过程。

### 为什么要过滤 `<uploaded_files>`

在 human 消息里，如果包含：

```text
<uploaded_files>...</uploaded_files>
```

会被先清掉。

原因是这些内容是会话临时状态，不应该进入长期记忆。

---

## 14.9　纠错检测

逻辑在：

- `internal/middleware/memory/llm.go` `detectCorrection()`

系统会检查最近的人类消息中是否出现显式纠错信号，例如：

- `that's wrong`
- `you misunderstood`
- `try again`
- `redo`
- `不对`
- `你理解错了`
- `重试`

如果检测到，就会把这次 run 标记为 `correctionDetected=true`。

这个信号会影响后续提取：

- 引导模型把纠错保存为 `correction` 类 fact
- 没有 LLM 时，规则提取也会补一条高置信度纠错 fact

---

## 14.10　UpdateQueue：为什么要异步 + 防抖

`UpdateQueue` 定义在：

- `internal/middleware/memory/memory.go`

它不是简单队列，而更像是：

- 一个按 `threadID` 聚合的 pending map
- 一个全局 debounce timer

### Add 行为

```text
Add(threadID, messages, ...)
    ↓
如果同 thread 已有 entry，则覆盖为最新版本
    ↓
保留 correctionDetected 的合并结果
    ↓
重置 timer
    ↓
等待 DebounceDelay 到期后统一 process()
```

### 这样设计的原因

#### 1）不阻塞主对话
记忆更新可能要调用 LLM，不应拖慢用户当前响应。

#### 2）避免重复更新
短时间内同一线程可能连续运行多次，没必要每次都立刻落盘。

#### 3）只保留最新状态
同线程旧消息被新消息覆盖，避免旧状态覆盖新状态。

所以它本质上是一个 **eventual consistency** 的长期记忆写入器。

---

## 14.11　两层提取路径：完整更新器与降级回退

`extractAndSave()` 的逻辑在：

- `internal/middleware/memory/memory.go`

### 优先路径：完整更新器 `LLMMemoryUpdater`

文件：

- `internal/middleware/memory/updater.go`

它能输出一个 `MemoryUpdate` patch，包含：

- User 各 section 是否更新
- History 各 section 是否更新
- `newFacts`
- `factsToRemove`

这种模式最强，因为它能同时维护：

- 摘要型记忆
- 事实型记忆
- 冲突事实删除

### 回退路径 1：Fact-only LLM 提取器

文件：

- `internal/middleware/memory/llm_extractor.go`

如果完整更新器不可用或未生效，可以退回到只提取 facts。

### 回退路径 2：规则提取

文件：

- `internal/middleware/memory/llm.go` `deriveFactsFromMessages()`

如果连 fact extractor 也没有结果，则退回规则法，从最近 human 消息中抽出简单 fact。

### 重要的当前实现观察

当前 `builder.go` 默认接线里，能直接看到的是：

- `queue.SetExtractor(memory.NewEinoFactExtractor(...))`

但没有看到默认调用 `SetUpdater(...)`。

这意味着：

- 完整的 `LLMMemoryUpdater` 能力已经实现
- 但当前默认接线更偏向 **fact-only 提取路径**

这是阅读源码时非常值得注意的一点：

> “能力已实现” 不等于 “默认路径已启用”。

---

## 14.12　完整更新器如何工作

文件：`internal/middleware/memory/updater.go`

### 输入

- 当前 memory 文档
- 本次对话内容
- 是否检测到纠错

### 输出

`MemoryUpdate`：

```text
MemoryUpdate
├── user.workContext
├── user.personalContext
├── user.topOfMind
├── history.recentMonths
├── history.earlierContext
├── history.longTermBackground
├── newFacts[]
└── factsToRemove[]
```

### 为什么输出 patch 而不是整份 memory

这样做更稳：

- 不会因为 LLM 一次波动就重写整个文档
- 只在 `shouldUpdate=true` 时替换目标 section
- 方便局部更新和冲突控制

### Prompt 的设计重点

Prompt 模板也在 `updater.go` 中，重点强调：

- 区分 User / History / Facts 三层信息
- 显式识别纠错信息
- 对 `correction` 使用更高置信度
- 不要记录文件上传事件
- 支持移除被新信息推翻的 facts

这说明它不是“抽摘要”，而是在做**结构化长期记忆维护**。

---

## 14.13　ApplyUpdates：落盘前怎么合并

函数：

- `internal/middleware/memory/updater.go` `ApplyUpdates()`

主要动作：

### 1）更新 User / History section

只有满足：

- `shouldUpdate == true`
- `summary` 非空

才会真正覆盖对应 section。

### 2）删除 facts

通过 `factsToRemove` 按 ID 删除。

### 3）新增 facts

新增时会：

- 校验内容非空
- 过滤低置信度
- 按内容去重
- 补全 ID / 时间 / 来源 threadID

### 4）清洗上传事件残留

即使前面已经过滤过消息，落盘前仍会再次调用：

- `stripUploadMentionsFromMemory()`

避免上传文件类语句进入长期记忆。

### 5）更新 `LastUpdated`

只有真正发生了变化才刷新时间。

---

## 14.14　事实去重与裁剪

### 1）AddFact 去重

`JSONFileStore.AddFact()` 使用：

- case-insensitive
- substring dedup

如果新 fact 内容已被旧 fact 包含，则不再追加。

### 2）Deduplicate 去重

`JSONFileStore.Deduplicate()` 会删除被更长事实“支配”的短 fact。

例如：

- `用户偏好 Go`
- `用户偏好使用 Go 开发后端服务`

第二条可能会让第一条变成 dominated fact。

### 3）最大事实数限制

当 facts 超过 `maxFacts` 时，会：

- 先按 `Confidence` 降序排序
- 再截断到前 `maxFacts`

这里和注入逻辑不同：

- **存储裁剪：按置信度优先**
- **注入选择：按近期性优先，再受 token 限制**

这是一个很容易读混的点。

---

## 14.15　配置项

配置定义在：

- `internal/config/config.go`

| 配置项 | 默认/含义 | 说明 |
|---|---|---|
| `enabled` | 是否启用 | 控制记忆 middleware 是否接入 |
| `storage_path` | `memory.json` | 记忆 JSON 文件路径 |
| `debounce_seconds` | 通常为 30 | 更新防抖时间 |
| `model_name` | 为空时走默认模型 | 用于记忆提取的模型 |
| `max_facts` | 通常为 100 | 存储中保留的最大 fact 数 |
| `fact_confidence_threshold` | 通常为 0.7 | 低于此阈值的 fact 会被过滤 |
| `injection_enabled` | 是否启用 | 是否把记忆重新注入 prompt |
| `max_injection_tokens` | 通常为 2000 | 注入阶段的 token 预算 |

`builder.go` 会从这些配置里完成：

- memory store 初始化
- queue 初始化
- debounce/maxFacts 配置
- fact extractor 配置
- 注入开关与 token 预算配置

---

## 14.16　Memory API：为什么它还提供人工编辑能力

文件：

- `pkg/gateway/handlers/memory.go`

除了自动记忆，它还暴露了完整 API：

- `GET /api/memory`
- `POST /api/memory/reload`
- `DELETE /api/memory`
- `POST /api/memory/facts`
- `PATCH /api/memory/facts/:fact_id`
- `DELETE /api/memory/facts/:fact_id`
- `GET /api/memory/export`
- `POST /api/memory/import`
- `GET /api/memory/config`
- `GET /api/memory/status`

这说明它把 memory 当成一个：

- 可自动提取
- 可人工校正
- 可导入导出
- 可前端面板化展示

的长期用户模型，而不是黑盒机制。

---

## 14.17　从测试里能确认哪些真实行为

建议重点看：

- `internal/middleware/memory/integration_test.go`

可以确认几个实现事实：

### 1）低置信度 fact 不会落盘

低于阈值的 fact 会被过滤掉。

### 2）默认只注入最新 15 条 facts

测试明确验证了 `fact-6 ~ fact-20` 会被注入，而不是按置信度最高的 15 条。

### 3）支持关闭注入

当 `WithInjectionEnabled(false)` 时，即使 memory 中已有 facts，也不会注入到 prompt。

这也提醒我们：**理解系统时要以源码和测试行为为准，而不是只看文档描述。**

---

## 14.18　这套设计的优点

### 优点 1：不阻塞主链路

更新异步执行，不影响当前对话响应速度。

### 优点 2：长期记忆边界清晰

明确过滤：

- 工具消息
- 中间 assistant 消息
- 上传文件内容
- 上传事件相关句子

### 优点 3：同时支持摘要型记忆与事实型记忆

既能保留连续语义，又能保留可结构化检索的信息点。

### 优点 4：有降级路径

完整更新器不可用时，仍可退回 fact-only，再退到规则提取。

### 优点 5：可人工修正

通过 API 可以直接查看、编辑、导入导出 memory。

---

## 14.19　当前实现值得注意的局限

### 1）默认主路径更偏 fact-only

完整 updater 已实现，但默认 builder 接线目前更明显地接入了 extractor。

### 2）去重主要是字符串级，而不是语义级

轻量、可用，但对同义改写不够强。

### 3）注入策略是“近期优先”，不是“相关性优先”

这会更强调最近信息，但未必总是最相关信息。

### 4）存储是单文件模型

适合当前单机/轻量部署，但未来若做多用户、多租户、分 agent 作用域，可能需要更细的 memory scope。

---

## 14.20　推荐阅读顺序

### 第一轮：先看入口与接线

1. `internal/middleware/builder/builder.go`
2. `internal/middleware/memory/memory.go`
3. `internal/middleware/memory/facts.go`

目标：先看清“它被怎么接进系统”以及“memory 长什么样”。

### 第二轮：看注入链路

4. `internal/middleware/memory/query.go`
5. `internal/middleware/memory/memory.go` 中的 `BeforeModel()`

目标：理解 memory 如何进入 prompt。

### 第三轮：看更新链路

6. `internal/middleware/memory/llm.go`
7. `internal/middleware/memory/memory.go` 中的 `AfterAgent()`
8. `internal/middleware/memory/memory.go` 中的 `UpdateQueue`

目标：理解一次 run 结束后，为什么不是立即写 memory。

### 第四轮：看提取与合并

9. `internal/middleware/memory/llm_extractor.go`
10. `internal/middleware/memory/updater.go`

目标：理解“从对话到记忆 patch”的过程。

### 第五轮：看管理能力

11. `pkg/gateway/handlers/memory.go`
12. `internal/middleware/memory/integration_test.go`

目标：理解 memory 的可观测性和真实运行行为。

---

## 14.21　你在学习时最值得追问的 5 个问题

1. 为什么记忆更新放在 `AfterAgent()`，而不是 `AfterModel()`？
2. 为什么 Memory 结构要分 `User / History / Facts` 三层？
3. 为什么 UpdateQueue 要做成“按 threadID 覆盖 + 全局防抖”？
4. 为什么系统反复过滤上传文件和工具消息？
5. 当前默认接线里，完整 updater 和 fact-only extractor 谁才是主路径？

如果把这 5 个问题搞清楚，这一块设计就已经真正吃透了。

---

## 14.22　一句话总结

GoClaw 的长期记忆系统，本质上是一个挂在 middleware 上的长期用户建模机制：

- 在读路径上，它把 memory 注入模型上下文
- 在写路径上，它把一次完整 run 的有效对话异步提炼成用户摘要与结构化事实
- 在工程实现上，它通过文件存储、防抖队列、降级提取与可编辑 API，形成了一个可持续、可观测、可修正的长期记忆闭环
