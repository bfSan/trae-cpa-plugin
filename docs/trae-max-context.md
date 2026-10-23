# Trae Max 上下文与思考档位：只读协议研究

研究日期：2026-10-23（Asia/Shanghai）。范围：CN / SOLO 当前 `llm_utils_chat + solo_work_lite`、官方 `solo_agent` 目录、官方 Web remote 请求链，以及插件 Intl 的实现差异。

> **结论：官方 DeepSeek-V4.1-Flash 确有 1M Max，上游能力依 function / 通道而异。当前插件 CN 通道尚未找到可验证的 Max 请求契约，不能仅把目录改成 solo_agent、把 CPA 上下文改成 1M，或传 `max_tokens=1000000` 来宣称支持。**
>
> 本研究不修改业务代码，不运行真实聊天、登录、refresh、签到或付费压力请求。两份脱敏目录由父级/backend 提供；研究者只读核对。父级授权仅新增本文档。没有安装、执行官方客户端。

## 1. 最重要的现场对照：同一个模型，换 function 即出现 Max

| 目录请求 function | config_name | context_window_tokens | display_config.max_mode | 可得结论 |
|---|---|---|---|---|
| `solo_work_lite` | `deepseek-v4.1-flash` | `dev:200000`，未给 `max` | `false` | 当前插件目录表达的是该通道普通能力，不能据此否认整个官方产品 Max |
| `solo_agent` | `deepseek-v4.1-flash` | `dev:200000,max:1000000` | `true` | 官方 agent 目录明确存在该模型 1M Max；无需猜另一个 Max config_name |

现场证据：

- [solo_work_lite 脱敏目录摘录](evidence/trae-max-context-comparison.json)：200K、Max false。
- [solo_agent 脱敏目录摘录](evidence/trae-max-context-comparison.json)：同 config_name，200K / 1M、Max true。
- 父级/backend 报告本次单变量请求保持身份与 headers，只改 function，目录总数由 47 增至 67。此前请求使用插件现有 IDE version `0.1.61` / version code `20260820`。**因此已经有 function 维度的直接差异证据，不需要把“客户端版本太老”当作唯一解释。**
- 用户提供的官方客户端截图显示模型已升 1017、思考强度“轻 / 高 / 极高”、Max 关闭，提示开启扩至 1M 并增加积分。这与 `solo_agent` 目录吻合，但截图不是网络请求抓包，不证明 `llm_utils_chat` 的具体参数。
- 用户看到的 `.12 / 错峰 .06` 可在两个目录的 `display_contact_config.activity_discount` 找到。同模型 / 费率相同不意味着不同 function 的上下文及能力完全相同。

### 脱敏 fixture 的负证限制

原 probe 的字段白名单没有保留 `reasoning_effort_config`。因此“fixture 未见该字段”**不能**推出“上游原始响应没有该字段”。同理，没有读取或保留的加密 model detail 不能凭空还原。本文不输出 token、账号标识或加密参数正文。

## 2. 当前 CN 插件为什么不能直接切过去

### 2.1 现有请求通道和历史实测

- [constants.go:26](../plugins/trae/upstream/constants.go#L26)：聊天端点 `/api/agent/v3/llm_utils_chat`。
- [variant.go:20–43](../plugins/trae/upstream/variant.go#L20-L43)：记录历史实测该端点只接受 `solo_work_lite`；把 CN 的旧 function 改为此值后恢复可用。
- [client.go:453–478](../plugins/trae/upstream/client.go#L453-L478)：记录 `solo_agent / llm_raw_chat` 的独立服务通道，以及部分 config 在 `solo_work_lite` 流内 4001 的精确过滤。`DeepSeek-V4-*-Official` 和不带 Official 的键有不同服务可用性，不能按名称大范围推断。

这些是仓库已有历史实测记录，本轮没有重新发聊天验证。**目录请求接受 `function=solo_agent`，不等于聊天端点接受该 function。**

### 2.2 当前 PrepareBody 是白名单，不是透传

[PrepareBodyResolved:183–239](../plugins/trae/upstream/payload.go#L183-L239) 重新构造请求，只保留：

- `messages,function,stream,config_name,model`；
- `tools,tool_choice`；
- 采样字段、`max_tokens`、`stop`；
- 后续单独添加的 `reasoning_effort`。

所以新添 `max_mode`、`mode_type`、`agent_type`、`config_info`、`current_config_info`、`custom_model` 或 `model_selection_strategy`，若只写入客户端 body，会在现白名单处丢弃。反过来，**仅放宽白名单也不证明上游支持该字段**，agent 字段已有触发 4023 的历史记录。

[FetchModels:491–559](../plugins/trae/upstream/client.go#L491-L559) 请求 `get_detail_param`，function 来自 `FunctionFor`，`mode_type` / `agent_type` 为 null；对外普通 `ContextWindow` 仍取 `contexts["dev"]`。metadata 工作保留其他 context 值是信息保真，不是开通 Max。

### 2.3 `max_tokens=1000000` 不是输入上下文证据

[默认 max_tokens:212–217](../plugins/trae/upstream/payload.go#L212-L217) 是已有输出参数默认值；注释混用了“128k 上下文”和输出截断措辞，不能拿来当本轮输入窗口证据。

输入上下文的直接证据是目录 `context_window_tokens.dev/max` 与实际 lane / mode 的选择链。修改输出上限既不改变当前 `solo_work_lite` 的目录能力，也不能替代 Max mode 请求。

## 3. 官方 Web remote：已找到完整的 UI → request 契约

官方入口：[work.trae.cn](https://work.trae.cn/)。无需登录即可读取其公开 JS。

下列行号是官方原始压缩 bundle 的物理行号；offset 为 Python 解码后的 **0-based 字符位置**，不是文件字节偏移。CDN hash 固定，便于复核。

### 3.1 证据索引

**Bundle A：** [8974.b76c7073.js](https://lf-cdn.marscode.cn/obj/marscode-bucket-cn/goofy_deploy_edenx/ide/trae-web-mono/static/js/8974.b76c7073.js)

| 位置 | 方法 / 标识 | 证据 |
|---|---|---|
| L82，offset 582283 附近 | `model.listModels` endpoint map | `GET /api/remote/v1/models` |
| L113，offset 1598899 | `fetchModels()` | query 参数 `functions:"solo_agent_remote,solo_work_remote,solo_design_remote",show_custom_model:true` |
| L113，offset 1597728–1598828 | `convertModelToModelConfig` / `convertToRawModelListMap` | 响应 `data.list[{function,models}]`；模型对象保留原字段，`config_name=e.name` |
| L113，offset 1328001 | mode enum | `Manual=0, Auto=1, Max=2`；仅证明 core mode 枚举，不可当 utils wire 参数 |
| L113，offset 1328862 | model Max capability helper | `model.max_mode === true` |
| L109，offset 993658–995387 | `getModelRequestSelection` | 校验所选模型 Max 能力及开关；返回 `modelSelectionStrategy: !skipAuto && maxOpen ? "max" : "manual"` |
| L109，offset 994491–995281 | `getModelRequestSelection` 的 custom model 构造 | `config_name/model_name/config_source/provider/prompt_max_tokens` 等来自模型记录；reasoning resolver 决定发送字段 |
| L109，offset 997377–998656 | `buildSendMessageRequest` | 把 `model_selection_strategy` 与 `custom_model` 放入最终消息 body |
| L109，offset 1000156–1000654 | `createSessionAndSendMessage` | 同一消息 builder 的产物放入 `initial_message`，提交创建会话 |
| L82，offset 576866 附近 | `chat.createSession` endpoint map | `POST /api/remote/v1/chat_sessions` |
| L82，同 endpoint map | `chat.sendMessage` | `POST /api/remote/v1/chat_sessions/:chat_session_id/messages` |
| L113，offset 1305635 | reasoning validator | 要求 `reasoning_effort_config.support_thinking`、非空无重复 options、default_level 属于 options |

**Bundle B：** [main.8fdc02ed.js](https://lf-cdn.marscode.cn/obj/marscode-bucket-cn/goofy_deploy_edenx/ide/trae-web-mono/static/js/main.8fdc02ed.js)

| 位置 | 标识 | 证据 |
|---|---|---|
| L1425，offset 2236169 | UI context renderer `K` | `getModelMaxModeOpen(model) ? context_window_tokens.max : context_window_tokens.dev` |
| L1425，offset 2231352 | reasoning renderer `V` | 从 `reasoning_effort_config` 读取 options/default_level，而非只有 reasoning enable 布尔值 |
| L1425，offset 2230693 附近 | label map `Z` | 支持 `low/light/medium/high/extra_high/xhigh/max/none`，其中 `extra_high` 和 `xhigh` 共用 Extra high 文案 |

### 3.2 remote 的最小线协议结构

以下为对官方代码的结构化摘要，不是真实请求，也未发送：

```json
{
  "mode": "code",
  "initial_message": {
    "model_name": "<目录中的name>",
    "agent_type": "solo_agent_remote",
    "model_selection_strategy": "max",
    "custom_model": {
      "config_name": "<目录中的config_name或name>",
      "model_name": "<目录中的name>",
      "config_source": 1,
      "is_preset": true,
      "provider": "<目录值>",
      "use_remote_service": true,
      "prompt_max_tokens": "<目录值，存在时>",
      "reasoning_effort_level": "<目录允许的选中值>"
    }
  }
}
```

注意：

1. 已证实的 Web Max 开关是 **`model_selection_strategy:"max"`**，不是简单增大 `max_tokens`。能力门控是模型 `max_mode`，展示窗口是 `context_window_tokens.max`。
2. 当前代码在 Max 分支没有给 config_name 加 `-max` 后缀；`config_name` 仍来自所选模型。不能臆造另一个 Max 模型 ID。
3. `prompt_max_tokens` 是目录模型属性的复制，不是本研究填入的 1M，也没有看到此 builder 因开关直接把它赋成 1M。**选择 Max 的请求字段与 token 限额字段不能混为一谈。**
4. 核心 resolver 默认把所选思考档位写入 **`custom_model.reasoning_effort_level`**；host resolver 可以明确选择 `custom_model.reasoning_effort`。不应同时无条件发送两者。
5. `mode` / UI 数字枚举 / `model_selection_strategy` / `agent_type` 属于不同层。不能因 `Max=2` 就直接向 utils 填 `mode_type:2`。
6. **上述是 Web remote 契约，不是桌面 `llm_raw_chat` 的完整契约，更不是 `llm_utils_chat` 的契约。** Web builder 的完整性也不能替代账号、地域、function 的服务端能力目录。

### 3.3 Thinking 档位：已证实与未证实

已证实官方通用 UI 支持“轻 / 高 / 极高”所需的标签集合，且等级是目录驱动。未证实 DeepSeek-V4.1-Flash 在目标 CN 通道的具体 options 是 `light/high/extra_high`、`low/high/xhigh`，还是其他组合。用户截图只有中文标签，两个“极高”英文键均存在，所以不能凭截图硬编码映射。

当前 CN [reasoning_effort 透传:219–227](../plugins/trae/upstream/payload.go#L219-L227) 只证明插件会发送非 auto/none/off 值；不证明上游采用相同官方档位语义，也不证明“容忍此字段”等于“该字段生效”。

## 4. Intl 现代码与官方 remote 的差异

插件 Intl 确实走 remote，所以是未来适配候选，但不能用来冒充当前 CN 账号问题已经解决。

- [Intl endpoint/base:30–61](../plugins/trae/intlupstream/client.go#L30-L61)：`https://core-normal.trae.ai/api/remote/v1`，默认 origin `https://work.trae.ai`。
- [resolveMode:156–174](../plugins/trae/intlupstream/client.go#L156-L174)：只有 auto / manual；没有 Max 分支。
- [CreateSession:249–269](../plugins/trae/intlupstream/client.go#L249-L269)：`initial_message` 已含 `model_selection_strategy`，但无 `custom_model`、思考档位 / 能力输入。
- [FetchModels:703–743](../plugins/trae/intlupstream/client.go#L703-L743)：只解码 `data:[{name}]`，没有官方现用 `data.list[{function,models}]` 结构，也没有解析 Max、context、reasoning capabilities。

后续可用离线 fixture / `httptest` 验证：两种目录信封兼容、按 function 分组、保留能力、从 explicit request option 选 `max`、在正确的 `initial_message.custom_model` 放等级。但**本轮没有 Intl 账号真实目录，没有验证 Intl 服务端 1M，也没有实现 adapter**。

访问 [work.trae.ai](https://work.trae.ai/) 能得到其独立 CDN 脚本列表；国际版脚本本轮读取遭连接重置。因此本文的直接 JS 证据来自 CN Web，不能声称已完成两个地域 bundle 一致性验证。

## 5. 桌面客户端检查：到哪里为止

官方下载元数据：[latest manifest API](https://api.trae.cn/icube/api/v1/native/version/trae/cn/latest)。研究时公开 CN IDE 为 `3.4.4 / build 2.3.93438`。

检查资源：[官方 Linux x64 tar.gz](https://lf-cdn.trae.com.cn/obj/trae-com-cn/pkg/app/releases/stable/2.3.93438/linux/TRAE_CN-linux-x64.tar.gz)。仅流式读取压缩包，未安装、未执行、未写入共享源码；包较大，已按父级要求停止继续下载 / 重复扫描。

已看到包内路径及位置：

- 包内 `./resources/app/product.json` 前几行：版本为上述 3.4.4 / 2.3.93438。
- 包内 `./resources/app/extensions/ai-completion/dist/extension.js`，L2，offset 3653965 附近：配置 `fusionModelDetailAPI:"api/ide/v1/get_detail_param"`、`fusionRawChatAPI:"api/ide/v2/llm_raw_chat"`、`llmRawChatAPI:"api/ide/v1/llm_raw_chat"`。
- 同 bundle L2，offset 813463 附近：`ModelScene.Agent="llm_raw_chat"`。

以上只证明官方存在另一套 raw chat 接口配置，**未追到 local Max UI → mode/config → 加密参数 → raw body 的完整调用链**。首轮只扫描了 `.js`，没有覆盖全部 `.mjs/.cjs` AI 包，不能据未命中否认能力；也不将通用 endpoint 配置冒充实际被调用的 CN agent lane。

## 6. 开源适配器交叉核对：未发现 utils Max 正证

核对同一社区仓库的 3 份文件，没有扩大到账号、压力测试或新真实请求。链接 main 分支会变化；本轮记录了读取时 blob SHA。

1. [Ttungx/trae-solo-local-api：src/trae-client.js](https://github.com/Ttungx/trae-solo-local-api/blob/main/src/trae-client.js#L425-L474)
   - blob `a5aaa2e4fefa1bf18744349201e858b970b2c6ac`。
   - L429–434 给 utils 设置 config_name/model；L432 明确注释不要加 `agent_type/device_id/ide_version`，历史实测会 4023。
   - L444–469 只转发输出 / sampling 参数；L474 为 `/api/agent/v3/llm_utils_chat`。
   - [L639–718](https://github.com/Ttungx/trae-solo-local-api/blob/main/src/trae-client.js#L639-L718) 的 `mode_type`、`custom_model.prompt_max_tokens`、`encrypted_model_params` 和 `current_config_info` 属于**另一方法的 `create_agent_task`**，不是 utils；不能看到同文件有这些字段就挪入 utils。
2. [SOLO_PARITY.md:36–49](https://github.com/Ttungx/trae-solo-local-api/blob/main/SOLO_PARITY.md#L36-L49)
   - blob `b64e474d9ff7c3a52d3923edce0212ce404c9077`。
   - 明确将指定模型 utils + solo_work_lite，与未完整复刻的完整 agent 分开。文档旧模型列表与我方后续实测可能有时效差异，不把它当当前全集权威。
3. [doc/think-effort.md:3–9](https://github.com/Ttungx/trae-solo-local-api/blob/main/doc/think-effort.md#L3-L9)
   - blob `d95dbc2fd2ed9c5fcce6cf36885e5baecc6c5911`。
   - 2026-07-23 方案采用 system prompt 前缀模拟思考强度，不是官方原生思考档位，更不是 Max 输入上下文开关。

结论是“本轮检查范围未找到 utils Max 的明确实现证据”，不是数学证明其永远不可能支持。

## 7. 本轮最小建议与下一阶段准入条件

### 本轮应该做

1. 保持现 CN `solo_work_lite / llm_utils_chat` 聊天协议；显示普通上下文 200K。
2. 可在说明中精确标记：“官方 `solo_agent` 目录声明 1M Max；当前插件通道尚未接通。”不要对当前可用模型静默宣称 1M，也不要把这写成全平台“不支持 Max”。
3. 把目录能力与实际聊天 lane 绑定；同 config_name 不能跨 function 覆盖 / 合并成一个无来源的最大值。
4. 不变更输出 `max_tokens` 来解决输入窗口，不靠放宽 PrepareBody 白名单盲透传猜测字段。
5. 本轮正常开展活动倍率 / metadata / UI 工作；Max 不假落地。

### 下一阶段有两条独立路线

**A. 保留 local agent 语义：**针对官方 `solo_agent / llm_raw_chat` 新建独立 adapter；先拿到完整的本地调用链，明确 mode_type 的 wire 值、current_config_info/config_info、自定义模型和加密参数选择、请求签名/headers、流式响应、取消与工具调用契约。不能仅把现 utils 的 function 改成 solo_agent。只读 metadata 可验证声明能力，不能最终证明长输入执行。

**B. 官方 remote：**复用已知 `model_selection_strategy:"max"` + `initial_message.custom_model` 的正式会话协议，但需要目标 CN 账号/地域的 remote models 权限和模型能力确认，并评估远程 agent 的任务/工具执行语义、计费与现纯聊天兼容性。这是独立功能，不是本轮 metadata 小改。Intl adapter 只对 Intl 使用者有意义，不能解决目前只有 CN 账号的问题。

在获得明确目标 endpoint / function 前，不建议继续盲探 metadata。若后续必须再取一次脱敏目录，应先确定最终白名单：`function`、`config_name/name`、`config_source/provider`、`display_config`、`context_window_tokens`、`max_mode`、`reasoning_effort_config{support_thinking,options,default_level}`、旧兼容 `reasoning_effort_options/default_reasoning_effort`、`prompt_max_tokens`，以及必要的 model-detail **字段名、非敏感限额和模式标识**；不输出 token、原始全响应或加密参数正文。

只读及离线测试可以完成契约建模；没有授权过的真实聊天测试，就必须把“实际 1M 输入可执行性 / 账单倍率”保留为未验证边界。
