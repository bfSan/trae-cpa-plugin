# 每日模型目录与只读上下文能力

> 当前策略以本文最后的 **0.13.5** 节为准。早期“仅实测模型扩大窗口”的0.13.2策略已经被账号目录最大声明策略替代；实测事实仍独立保留。

## 用户确认的边界

- 只使用 CPA 本机现有凭证和上游接口，不读取、监控或导入桌面 SQLite。
- 模型目录在后端每 **24 小时**刷新；账号“刷新凭证”、宿主 `auth.refresh` 和手动“刷新目录”是额外触发入口。
- 上下文、思考能力只读展示。蓝色加粗表示当前 CPA 声明或有证据的默认值，不是点击选档或 Max 开关。
- 账号调度、层级和优先级仍归 staged/CPA；本次不调整线上规则。

## 每日刷新行为

按每个账号的 `model_refresh_state.last_attempt_at` 计算下一次到期，不能把整个区域第一个账号视为全部账号。维护循环每分钟仅检查是否到期；真正的目录请求最多每日一次，手动入口除外。页面打开与否不影响后台执行。

旧 schema 或没有目录的账号首次发现时更新一次。仅对启用且仍存在的凭证运行；目录查询不主动轮换 token。缺 token、过期或请求失败会保存失败状态、保留自己的旧目录，等待下一次日更新或显式凭证刷新，不无限重试。

多个调用按账号合并。网络操作不持账号锁；提交前重读身份、token、refresh token、渠道、主机和 disabled 状态。变化则丢弃旧结果。HostSave 触发的同步模型注册回调不能等待同一个刷新任务，避免自锁；并发 token 更新导致的目录重试最多一次。

凭证刷新成功但目录失败时，保留已保存的新 token，返回模型刷新单独状态，不把整个凭证刷新误报失败。`model_refresh` 包含 `updated/status/error/fetched_at/next_refresh_at`；`partial` 表示主目录已更新、辅助能力目录失败。

宿主 ABI 不提供跨外部写入者的事务 CAS，提交前检查与同插件锁不能消除最后一次独立宿主写入的竞态。

## 目录来源与字段

CN/SOLO 主目录仍以 `get_detail_param(function=solo_work_lite)` 决定可调用模型。额外读取同一账号的 `function=solo_agent` 目录，只按完整配置 ID 补充能力；不会加入仅 agent 能用的模型，也不会把该来源的活动价格当成 worklite 报价。

每个 `capability_catalog` 项携带自己的 `source_function`，保留：

- 普通、扩展上下文及上游真实档位数组；多个普通档不任选一个作默认。
- `max_mode` 等声明标志。
- `prompt_max_tokens`：上游报告的输入预算，**不是已证实的计费基线**。
- 输出限制（若明确报告），缺失保持未知。
- 推理支持、档位原始 wire 值和明确默认值；辅助来源默认不会成为当前请求通道默认。

辅助目录失败不清除成功主列表，返回部分成功。旧辅助能力不会被标成这次新取得的数据。Intl 继续使用自己的目录来源，未取得能力的字段保留未知，不能借用 CN 数据。

模型元数据日更新后，活动和倍率代表快照。前端不会因为活动窗口跨界自动发上游请求；已知结束日期仍可本地标为结束，旧的当前报价可以标为“快照／待刷新”，不伪装实时价格。

## 1M 级上下文的依据

[四档脱敏结果](evidence/trae-worklite-context-tests.json)记录当前 CN 账号、`deepseek-v4.1-flash`、原 `llm_utils_chat + solo_work_lite` 接口的结果：

| 上游报告输入 tokens | 三位置检索 | 正常结束 |
|---:|---|---|
| 3,406 | 全对 | 是 |
| 240,246 | 全对 | 是 |
| 901,421 | 全对 | 是 |
| 990,767 | 全对 | 是 |

没有设置 Max 策略或上下文覆盖；输出上限为 256。实际最高总用量是 990,895 tokens。990,767 是实测输入下界，不是已测出的硬上限，也不是百万输入加大输出同时成功的证明。三位置标记排除了明显的简单截断，不声称证明上游内部永远没有任何压缩。

[声明目录对照](evidence/trae-max-context-comparison.json)另确认同模型 agent 目录的扩展值是 1,000,000。因此本次将该账号的有效 CPA 声明设为 1M 级，仍须为系统、工具和输出留预算。**不修改聊天 function、输出 max_tokens 或 Max 策略。**

## 凭证绑定的证据记录

证据保存在该账号文档顶层 `model_context_evidence`，不是仓库中硬编码的个人 UID 或对整个渠道的无条件规则。绑定项包括：

- realm、模型完整 ID、已验证的请求 function；
- 账号身份的 scope hash，防止复制记录给另一账号后自动生效；
- 上游报告的输入数量、三位置检索是否通过、观察时间、脱敏证据 ID；
- 对应声明窗口。

每次发现模型时都要求当前目录仍声明对应窗口，且凭证绑定、通道和模型匹配。缺失、不匹配、非法证据或没有相应当前声明不会自动扩大窗口。记录是管理员信任的配置证据，不是密码学签名认证；不宣称任意有文件写权限的操作者无法修改它。

历史实测不按无依据的固定天数失效，但展示观察日期，仍受当前目录及账号匹配约束。新账号和其他模型需要各自证据，不能自动继承这次验证。

客户端若另有固定 200K 历史压缩阈值，需要同步读取 CPA 有效能力；仅更新服务器模型声明不能强制第三方客户端改变自己的配置。

## 验证与部署

本版使用离线 Go/race、前端 DOM 和 Chrome fixture 验收；不重复执行上述付费长输入探针。部署时先停止宿主以避免凭证写入竞态，只给已测账号增加证据元数据，保留所有 token、人工禁用和其他扩展字段，再原子换库启动并验证实际模型快照与注册版本。


## 0.13.3 展示与操作更正

上下文普通/扩展档位不再用蓝色表现成“默认选中”；当前 CPA 声明和实测输入保留在提示信息中，不改变已生效的1M声明。只有明确来自当前请求通道的思考默认值可强调，能力文字仍只读。

每张账号卡仅一个“刷新”，沿用凭证、额度、模型的完整刷新。每个模型行仅一个“隐藏/恢复”，按当前真实渠道ID逐项保存到宿主排除规则，保留全局锁、通配规则和无关模型。完全隐藏的模型显示在末尾；原排序不因隐藏写入，恢复可回原位置。拖拽仅同一显示分区内排序。移除模型列表内部固定高度，避免长页面出现双重滚动或截图拼接断层。

活动不再笼统写“待核实/覆盖待核实”，而显示上游未提供时区、会员资格未确认、快照过期、时段已变化等具体原因。同账号、同活动种类、同价格、同时间窗口的当前优惠与候选活动去重；不同价格/会员条件仍保留。条件不足不冒称活动已生效。

Intl 授权页面备用地址改为官网 `https://www.trae.ai`，正常 GetLoginGuidance 和 API origins 不变。页面点击时同步预开脱离 opener 的窗口，拿到授权地址后再导航，避免异步后打开被浏览器拦截；保留手动打开链接。粘贴发起授权地址会明确提示尚未完成授权；回调的 amp 实体只解一次，不整体 URL 解码，保留签名字节与现有 pending。

公开官网的匿名浏览器检查可进入登录页，不能等同于真实用户账号的完整 OAuth 已成功；本版未自动完成用户登录。


## 0.13.4：WorkBuddy 样式与 Intl 交换状态修正

按用户最终确认，思考档位显示明确的 `high（默认）` 等标记，不展示“目录参考”；来源留在提示信息中，默认值必须是该账号目录明确声明且属于实际档位集合，账号间有冲突或覆盖不足时不猜。上下文恢复简洁的 `200K / 1M`，**蓝色加粗代表当前 CPA 生效声明**，不是可点击开关。删除“普通/扩展”前缀，相同档位去重。此段替代上一节的上下文不高亮方案。

Intl 授权后未出现在列表的现场状态是回调已收到、exchange_started、无 Token/Result 检查点，未生成 Intl 凭证文件。旧版本将原始错误覆盖为笼统的 outcome uncertain，不能还原该次上游 HTTP 状态或断言某一个具体故障就是原因。

修复可确定的解析缺陷：交换响应预检允许顶层 token，但最终解析只读 Result，可能在成功 POST 后丢弃 token。现在预检与最终解析统一选择 Result/result/顶层中的同一个有效 envelope；12种字段/包裹组合有测试。HTTP 2xx 的业务 Error 不视为登录成功，数字型业务码可识别。

交换失败持久化只含允许的 category/http_status/biz_code，不存上游错误文本、URL 或请求参数。终止失败返回 restart_required=true、pending=false；已有 token/result 的任务仍可继续保存。接到回调的页面不再提前声称登录完成。授权码是一次性的，不能因失败、404或重启不确定状态而自动再次 POST。

此次修复不等于用户原任务已恢复或 Intl 账号已保存。旧任务无可恢复 token 时，需要用户新授权；下一次失败会留下脱敏可追查状态。本版没有代用户发起或完成真实登录。


## 0.13.5：最大声明实际生效与 Intl 设备绑定

### 上下文：修正注册政策，而不只改高亮

此前只对带实测receipt的Deepseek扩大上下文，导致其他模型虽然已读到max=1M，实际仍注册200K/256K。这不符合用户希望按已知能力生效的要求。现在，同账号同次发现中，仅对主调用列表中精确ID匹配的模型，采用当前来源明确声明的合法最大窗口；该来源max_mode=false则不采用其max。缺max不猜，数组取合法最大值，不硬编码1M，也不增加只有辅助来源才授权的模型。

实际注册、持久化model_cache.models及管理列表的context_length一致提升；原始facts.context/contexts.dev和capability_catalog保留。实测记录只附加到真正验证过的账号/模型，目录1M不冒充逐模型长输入实测。没有改变chat function、Max策略或输出max_tokens。

CN/Solo目录schema升级为3，旧成功快照重新读取一次；失败/partial/in_progress仍遵循24小时节流，失败保留旧快照。Intl维持schema2，不借用CN能力。

### Intl：授权设备与交换设备保持一致

确认的协议差异：旧Intl start在授权URL使用安装身份，浏览器GetPCAuthCode将该DeviceID连同ClientID/PKCE提交；收到callback UID后，本地却把device/machine/公私钥切换成UID派生身份，再进行ExchangeToken。两个现场失败记录的脱敏比较均显示PKCE自洽、回调后身份是UID派生且不同于bootstrap、HTTP401、没有token检查点。该变化是401的高置信原因；由于旧版本没有保留具体业务码，不能宣称现场已证实20403。

新Intl授权在发起时创建独立设备身份，并贯穿URL、callback、pending持久化/恢复、交换及保存。不同授权任务不共用安装bootstrap；callback不再换身份。CN现有行为不改，旧授权码不重放。**每次新的Intl授权可能新增上游设备记录；已有凭证的正常refresh继续复用保存身份，不会随刷新创建新设备。**

参考来源：
- [cockpit-tools auth-code ExchangeToken请求](https://github.com/jlcodes99/cockpit-tools/blob/main/src-tauri/src/modules/trae_oauth.rs#L2253-L2289)：ClientID/AuthCode/CodeVerifier/DeviceInfo/IDEVersion；没有需要新增的DeviceProof、ClientSecret或req_source。
- [auth-code区域端点](https://github.com/jlcodes99/cockpit-tools/blob/main/src-tauri/src/modules/trae_oauth.rs#L727-L747)：普通Intl为SG，USTTP为US；不是按callback业务host选路。抓取的固定[源码blob](https://api.github.com/repos/jlcodes99/cockpit-tools/git/blobs/f34cf902bfc122c38d4bc056f333b09dada95d14)。
- [Trae官网授权页面脚本](https://lf16-web-neutral.traecdn.ai/obj/trae-ai-static/trae_website/static/js/async/6597.d2fb2c9ae3.js)：读取URL device_id并传给TraeGetPCAuthCode。

20403（设备不匹配）加入安全错误码白名单，其他任意上游文本仍不回显。面板正确识别Authorization callback received为“已收到待确认”，不再弹出误导性红色失败toast；真正交换失败仍明确显示。

离线mock覆盖普通/USTTP、URL到保存全流程身份一致、重启不换身份、两个新任务身份独立、重复完成仅一次POST。没有用真实用户授权码重放验证；实际成功登录仍需用户发起新的授权。


## 0.13.6：Intl 分组目录兼容

现场刷新报 `models parse: json: cannot unmarshal object ... into []struct{Name string}`。只读核对已保存 Intl 凭证的真实响应：HTTP 200、业务码 0，模型位于 `data.list[].models[]`（当前返回 6 个），而旧解析器只接受 `data[]`。这属于上游返回结构变化，不是登录或额度故障，凭证刷新本身已成功。

现在同时接受两种明确形状：

- 旧式 `data: [{name}]`；
- 新式 `data.list: [{function, models:[{name, context_window_tokens, max_mode, ...}]}]`，按组拼接后去重。

不递归探索任意对象，只读取上述字段；业务码非 0 仍在形状解析之前拒绝，避免把错误响应当成模型；限制响应 1 MiB、组数 128、模型数 4096、名称长度与换行字符，超限或结构未知直接报错而不是返回部分列表。

对之前已记录 `models parse: json: cannot unmarshal object` 的失败状态允许一次立即重试，避免被旧版 24 小时退避拖住；其他失败仍按原节流保留旧快照。目录 schema 统一为 3，`data.list[].models[].context_window_tokens.max` 与前节最大窗口策略一致，未改变模型授权范围。

本次未使用真实聊天、签到或重新登录，仅用现有凭证做只读目录查询。
