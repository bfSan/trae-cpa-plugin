# trae-cpa-plugin

[CLIProxyAPI (CPA)](https://github.com/router-for-me/CLIProxyAPI) 的 Trae 插件，统一覆盖 **CN、SOLO CN、Intl**。本仓库只构建、测试与发布 Trae，不再打包其他渠道插件。

## 职责边界

- **Trae**：上游协议、设备身份及签名、OAuth 与凭证维护、签到确认、真实额度与模型能力信息、当前账号请求执行。
- **staged-scheduler / CPA**：账号池管理、层级分发、优先级、成本排序、轮换、跨账号选择与故障转移。
- Trae 不持有策略账号池，不按余额选账号、不轮换账号、不设置四档策略冷却。兼容 `scheduler.pick` 仅返回不接管。
- 配额观察只记录上游拒绝的账号/模型事实；不是余额排序器。零余额不会自动禁用或删除凭证，手动解除观察也不会撤销人工禁用。

## 功能

- **三渠道 OAuth**：保留各自 ClientID、PKCE、设备密钥、DeviceProof 与回调协议。面板提供直接登录入口和远程回调粘贴，不使用要求通用 `state/code` 的错误入口。
- **登录恢复**：按会话保存阶段检查点；已获取 token 后，身份查询或保存失败可重试。安全存储有数量、大小、TTL、权限与原子写限制；不会承诺跨上游和本地磁盘的严格 exactly-once。
- **签到**：仅 CN/SOLO；手动、自动、重试使用同账号互斥。只有明确确认才显示已签；不确定领取只核实、不盲目再次提交。没有签到历史界面。
- **凭证维护**：通过宿主当前凭证清单和物理状态维护；人工禁用、删除、已有扩展字段受保护。奖励配置不作为可花余额。
- **统一网络出口**：OAuth、Token 刷新、签到、余额、模型和聊天遵循同一代理策略。显式代理失败不降级直连，已经尝试的宿主桥请求失败不重新直发。
- **模型与账号面板**：CN/SOLO/Intl 分列、真实上下文、账号差异、排序、隐藏/恢复、重命名和禁用管理。隐藏沿用宿主 `oauth-excluded-models`，没有第二份冲突配置。
- **真实能力边界**：未上报的倍率和思考默认值不猜测；单个上下文长度不伪装成可切换档位。`max_tokens` 是输出预算，不是输入上下文承诺。默认标签不加粗，只有真正选中项才高亮加粗。

## 安装与配置

从本仓库 Release 下载 `trae_<version>_<goos>_<goarch>.zip`。解压后仅安装当前平台的 Trae 动态库：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    trae:
      enabled: true
      login_variant: cn       # 新登录的默认类型；已有账号维持自己的渠道
      checkin_auto: true
      token_keepalive: true
      # proxy-url: http://127.0.0.1:7890
      # callback_bind: 127.0.0.1
      # callback_port: "41890"
      # model_order: [model-a, model-b]
```

空 `proxy-url` 继承 CPA 宿主网络路径；非法显式代理配置会阻止出站请求，不让插件悄悄直连。当前宿主桥不提供完整逐请求 deadline/redirect 控制，不能把插件侧超时误称为端到端硬超时。

商店源：

```text
https://raw.githubusercontent.com/bfSan/trae-cpa-plugin/main/registry.json
```

**更新已加载的动态库时，不得原地覆盖文件内容**：写到同目录临时文件，原子换名（新 inode），重启 CPA，并验证插件注册版本、加载 inode 和健康状态。不要同时安装统一插件与同渠道的旧拆分插件。

## 开发与构建

需要 Go 1.26+、C 编译器、Node.js（面板测试）与 Python 3（打包）。

```bash
make -C plugins/trae test
make -C plugins/trae lint
./scripts/build.sh linux amd64
```

默认测试只使用 mock、临时目录和 loopback HTTP。真实上游探针位于 `live` build tag 后且需要显式环境开关，不属于发布验证。

目录：

- `plugins/trae/`：CPA 接口、管理面板和维护逻辑。
- `plugins/trae/auth/`：凭证格式与稳定身份。
- `plugins/trae/upstream/`：CN/SOLO 协议。
- `plugins/trae/intlupstream/`：Intl Web SOLO 协议。
- `plugins/trae/netroute/`：统一网络出口，不涉及账号选择。
- `docs/`：协议来源和可靠性/职责边界。

## 上游与许可证

本项目基于 [mmqz/cpa-multi-plugins](https://github.com/mmqz/cpa-multi-plugins) 中的 Trae 实现独立维护，保留 Git 历史及 MIT 许可证。协议来源包括：

- [Sliverkiss/traework2api](https://github.com/Sliverkiss/traework2api)：Trae CN/SOLO 协议基础。
- [cockpit-tools](https://github.com/jlcodes99/cockpit-tools)：渠道身份、签到、额度字段语义。
- [OmniRoute](https://github.com/diegosouzapw/OmniRoute)、[9router](https://github.com/decolua/9router)：Intl Web SOLO 会话与事件流。
- [Ttungx/trae-solo-local-api](https://github.com/Ttungx/trae-solo-local-api)：CN 请求字段兼容性参考。
- [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)：插件 SDK 与宿主接口。

[历史协议来源说明](docs/PROTOCOL.md)可能包含上游多插件仓库的旧记录，不代表本 fork 当前发布范围。许可证见 [LICENSE](LICENSE)。
