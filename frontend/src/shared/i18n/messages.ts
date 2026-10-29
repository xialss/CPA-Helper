import { currentLanguage, localize } from './language'

type MessagePair = readonly [zh: string, en: string]
type ServerMessagePattern = readonly [RegExp, (match: RegExpMatchArray) => string]

const exactServerMessages: MessagePair[] = [
  ['ultrafast 倍率仅支持 OpenAI 兼容或 Codex 渠道的 gpt-6.1-sol 和 gpt-6-astra', 'ultrafast multipliers only support gpt-6.1-sol and gpt-6-astra on OpenAI-compatible or Codex channels'],
  ['ultrafast 倍率必须是大于 0 的有限数值', 'The ultrafast multiplier must be a finite number greater than 0'],
  ['ultrafast 倍率会产生无法安全计价的金额', 'The ultrafast multiplier produces an amount that cannot be priced safely'],
  ['Fast 倍率必须是大于 0 的有限数值', 'The Fast multiplier must be a finite number greater than 0'],
  ['Fast 倍率会产生无法安全计价的金额', 'The Fast multiplier produces an amount that cannot be priced safely'],
  ['Fast 倍率仅支持 OpenAI 兼容或 Codex 渠道', 'Fast multipliers only support OpenAI-compatible or Codex channels'],
  ['Claude 缓存 TTL 配置已移除', 'Claude cache TTL settings have been removed'],
  ['TTL 缓存价格已移除，请使用统一缓存写入价格', 'TTL cache prices have been removed. Use the unified cache write price.'],
  ['当前 CPA Helper Plugin 不支持响应模型核验', 'The current CPA Helper Plugin does not support response model verification'],
  ['同步期间 CPA 凭据模型注册已变化，请刷新后重试', 'CPA credential model registration changed during synchronization. Refresh and try again.'],
  ['CPA 注册凭据与元数据不一致，请重新同步', 'CPA registered credentials do not match the metadata. Sync again.'],
  ['CPA 凭据元数据未被宿主注册目录完整覆盖，请重新同步', 'The host registered catalog does not fully cover the CPA credential metadata. Sync again.'],
  ['插件策略保存后未生效', 'Plugin policy was saved but did not take effect'],
  ['响应核验配置已提交，但插件未确认生效', 'Response verification config was submitted, but the plugin did not confirm activation'],
  ['插件策略已提交，但未确认目标修订号', 'Plugin policy was submitted, but the target revision was not confirmed'],
  ['CPA Helper Plugin 不可达', 'CPA Helper Plugin is unreachable'],
  ['CPA Helper Plugin 管理端认证失败，请检查管理密钥', 'CPA Helper Plugin management authentication failed. Check the management key.'],
  ['CPA Helper Plugin 未加载或不支持当前管理契约', 'CPA Helper Plugin is not loaded or does not support the current management contract'],
  ['CPA Helper Plugin 策略快照格式无效（groups/keys 必须为对象数组）', 'CPA Helper Plugin policy snapshot is invalid (groups/keys must be arrays of objects)'],
  ['CPA Helper Plugin 配置校验失败', 'CPA Helper Plugin configuration validation failed'],
  ['CPA Helper Plugin 写入缺少版本前置条件', 'CPA Helper Plugin write is missing the version precondition'],
  ['CPA Helper Plugin 配置已被其他管理端修改，请刷新后重试', 'CPA Helper Plugin configuration was changed by another administrator. Refresh and try again.'],
  ['插件配置已被其他管理端修改，请刷新后重试', 'Plugin configuration was changed by another administrator. Refresh and try again.'],
  ['CPA Helper Plugin 模型列表策略不可用', 'CPA Helper Plugin model list policy is unavailable'],
  ['CPA Helper Plugin 模型目录无效', 'CPA Helper Plugin model catalog is invalid'],
  ['CPA Helper Plugin 模型目录不可用，请同步目录', 'CPA Helper Plugin model catalog is unavailable. Sync the catalog.'],
  ['CPA Helper Plugin 模型目录已失效，请重新同步', 'CPA Helper Plugin model catalog is out of sync. Sync it again.'],
  ['CPA Helper Plugin 模型目录需要管理员同步，请检查管理配置', 'The CPA Helper Plugin model catalog requires an administrator to sync it. Check the management settings.'],
  ['CPA Helper Plugin 模型目录已被其他管理端修改，请刷新后重试', 'The CPA Helper Plugin model catalog was changed by another administrator. Refresh and try again.'],
  ['CPA Helper Plugin 模型目录已提交，但未确认目标修订号', 'The CPA Helper Plugin model catalog was submitted, but the target revision was not confirmed.'],
  ['CPA Helper Plugin 模型目录已提交，但未确认生效', 'The CPA Helper Plugin model catalog was submitted, but activation was not confirmed.'],
  ['同步期间 CPA 配置或凭据已变化，请刷新后重试', 'CPA configuration or credentials changed during synchronization. Refresh and try again.'],
  ['CPA 凭据模型目录格式错误', 'CPA credential model catalog format is invalid'],
  ['CPA Helper Plugin 模型列表查询失败', 'CPA Helper Plugin model list query failed'],
  ['CPA Helper Plugin 无法识别当前 API 密钥，模型列表未按路由规则过滤', 'CPA Helper Plugin cannot identify this API key; the model list was not filtered by routing rules'],
  ['CPA 模型列表包含无效模型标识', 'CPA model list contains an invalid model identifier'],
  ['CPA Helper Plugin 管理密钥无效', 'CPA Helper Plugin management key is invalid'],
  ['CPA Helper Plugin 管理契约不兼容', 'CPA Helper Plugin management contract is incompatible'],
  ['CPA Helper Plugin 版本不兼容', 'CPA Helper Plugin version is incompatible'],
  ['CPA Helper Plugin API 密钥启用状态无效', 'CPA Helper Plugin API key enabled state is invalid'],
  ['CPA Helper Plugin 返回的 JSON 不是对象', 'CPA Helper Plugin returned JSON that is not an object'],
  ['CPA Helper Plugin 策略字段格式无效', 'CPA Helper Plugin policy field format is invalid'],
  ['CPA Helper Plugin API 密钥 group_ids 字段格式无效', 'CPA Helper Plugin API key group_ids field format is invalid'],
  ['CPA Helper Plugin 规则字段格式无效', 'CPA Helper Plugin rule field format is invalid'],
  ['CPA Helper Plugin 当前并发数据缺失或无效', 'CPA Helper Plugin current concurrency data is missing or invalid'],
  ['CPA Helper Plugin 当前并发数据超出可表示范围', 'CPA Helper Plugin current concurrency data exceeds the supported numeric range'],
  ['CPA Helper Plugin 路由规则关联无效', 'CPA Helper Plugin route rule association is invalid'],
  ['CPA Helper Plugin 并发上限数据无效', 'CPA Helper Plugin concurrency limit data is invalid'],
  ['CPA Helper Plugin capabilities 缺少响应核验配置', 'CPA Helper Plugin capabilities are missing the response verification config'],
  ['scope 参数无效', 'Invalid scope parameter'],
  ['API KEY 不存在', 'API key does not exist'],
  ['API KEY 已过期，请先修改密钥有效期', 'API key has expired; update its expiration first'],
  ['密钥有效期必须是 RFC3339 日期时间或 null', 'Key expiration must be an RFC3339 date-time or null'],
  ['密钥过期时间必须晚于当前时间', 'Key expiration must be in the future'],
  ['当前 API KEY 缺少完整密钥，无法恢复', 'The full API key is missing; it cannot be restored'],
  ['CLIProxyAPI 地址不能为空', 'CLIProxyAPI URL is required'],
  ['CLIProxyAPI 地址无效', 'CLIProxyAPI URL is invalid'],
  ['CLIProxyAPI 地址缺少主机名', 'CLIProxyAPI URL is missing a hostname'],
  ['CLIProxyAPI 地址必须使用 http、https、tcp、redis 或 resp 协议', 'CLIProxyAPI URL must use http, https, tcp, redis, or resp'],
  ['CPA 模型列表响应不是有效 JSON', 'CPA model list response is not valid JSON'],
  ['CPA 模型列表响应格式不支持', 'CPA model list response format is unsupported'],
  ['CPA 模型列表响应缺少模型列表', 'CPA model list response is missing the model list'],
  ['当前 API 密钥缺少完整密钥，无法映射插件策略', 'The full API key is missing; its plugin policy cannot be mapped'],
  ['插件 API 密钥策略缺少 id', 'Plugin API key policy is missing id'],
  ['插件 API 密钥策略必须包含 policy_revision 和 key', 'Plugin API key policy must include policy_revision and key'],
  ['插件 API 密钥身份映射不匹配', 'Plugin API key identity mapping does not match'],
  ['插件目录响应格式无效', 'Plugin directory response format is invalid'],
  ['插件目录响应缺少 credentials', 'Plugin directory response is missing credentials'],
  ['CLIProxyAPI 配置响应不是有效 JSON object', 'CLIProxyAPI configuration response is not a valid JSON object'],
  ['CLIProxyAPI v8 配置缺少 access', 'CLIProxyAPI v8 configuration is missing access'],
  ['CLIProxyAPI v8 配置缺少 api-keys', 'CLIProxyAPI v8 configuration is missing api-keys'],
  ['读取 CPA API KEY 失败：v8 配置格式无效', 'Failed to read CPA API keys: invalid v8 configuration format'],
  ['CPA 认证文件目录格式无效', 'CPA auth file directory format is invalid'],
  ['CPA 凭据缺少宿主 ID', 'CPA credential is missing its host ID'],
  ['CPA 凭据目录存在重复标识', 'CPA credential directory contains duplicate identifiers'],
  ['CPA 配置与运行时凭据索引不一致', 'CPA configuration does not match the runtime credential index'],
  ['CPA 凭据索引格式错误', 'CPA credential index format is invalid'],
  ['CPA 上游 Key 格式错误', 'CPA upstream key format is invalid'],
  ['CPA 上游配置格式错误', 'CPA upstream configuration format is invalid'],
  ['峰谷时段模板不存在', 'Time pricing template does not exist'],
  ['峰谷时段时间必须为 HH:mm', 'Time pricing windows must use HH:mm'],
  ['请选择明确的 IANA 时区', 'Choose an explicit IANA time zone'],
  ['峰谷时区无效', 'Invalid time pricing time zone'],
  ['至少配置一个高峰时段', 'Configure at least one peak window'],
  ['高峰时段必须有星期且开始结束不可相同', 'Peak windows require weekdays and different start and end times'],
  ['星期必须在 0 到 6 之间', 'Weekdays must be between 0 and 6'],
  ['高峰时段不可重叠（包括跨午夜时段）', 'Peak windows must not overlap, including overnight windows'],
  ['长上下文空闲倍率必须为非负有限数', 'Long-context off-peak multiplier must be finite and non-negative'],
  ['空闲倍率必须为非负有限数', 'Off-peak multiplier must be finite and non-negative'],
  ['空闲价格必须为非负有限数', 'Off-peak rates must be finite and non-negative'],
  ['请完整设置空闲输入、输出和缓存价格', 'Set all off-peak input, output, and cache rates'],
  ['请完整设置空闲价格', 'Set all off-peak rates'],
  ['空闲定价方式无效', 'Invalid off-peak pricing mode'],
  ['峰谷定价仅支持 DeepSeek 渠道 Token 价格', 'Time pricing only supports DeepSeek channel token prices'],
  ['峰谷定价仅支持 DeepSeek 或 Claude 渠道 Token 价格', 'Time pricing only supports DeepSeek or Claude channel token prices'],
  ['1 小时缓存价格仅支持 Claude 价格', '1-hour cache prices are only supported for Claude prices'],
  ['请明确提供峰谷规则；null 表示关闭', 'Provide a time pricing rule explicitly; null disables it'],
  ['生效时间不能早于当前时间', 'Effective time cannot be in the past'],
  ['请选择渠道模型价格', 'Select channel model prices'],
  ['渠道价格不可重复', 'Channel prices must not be duplicated'],
  ['请选择 DeepSeek 渠道 Token 价格', 'Select DeepSeek channel token prices'],
  ['请选择 DeepSeek 或 Claude 渠道 Token 价格', 'Select DeepSeek or Claude channel token prices'],
  ['请求失败', 'Request failed'],
  ['复制失败', 'Copy failed'],
  ['没有可复制的内容', 'Nothing to copy'],
  ['复制失败，请手动选中后复制', 'Copy failed. Select the text and copy it manually.'],
  ['登录状态缺少角色信息，请重启后端服务后重新登录', 'Your session is missing role information. Restart the backend and sign in again.'],
  ['CPA 配置未完成：请先到「系统设置」填写 CLIProxyAPI 地址和管理密钥，再返回 API 密钥页操作。', 'CPA settings are incomplete. Fill in the CLIProxyAPI URL and management key in System Settings, then return to API Keys.'],
  ['读取 CPA API KEY 失败：返回格式无效', 'Failed to read CPA API keys: invalid response format'],
  ['服务器内部错误', 'Internal server error'],
  ['请求体不是有效 JSON', 'Request body is not valid JSON'],
  ['请先登录', 'Sign in first'],
  ['登录会话已失效', 'Your sign-in session has expired'],
  ['首次登录后必须先修改账号密码', 'Change the account password after first sign-in'],
  ['需要管理员权限', 'Admin access is required'],
  ['需要超级管理员权限', 'Super admin access is required'],
  ['需要超级管理员权限才能设置超级管理员', 'Super admin access is required to assign a super admin'],
  ['需要超级管理员权限才能设置用户额度', 'Super admin access is required to set user quotas'],
  ['需要超级管理员权限才能创建管理员', 'Super admin access is required to create an admin'],
  ['需要超级管理员权限才能修改超级管理员角色', 'Super admin access is required to change the super admin role'],
  ['需要超级管理员权限才能修改管理员角色', 'Super admin access is required to change the admin role'],
  ['普通管理员不能操作超级管理员账号', 'Regular admins cannot operate on super admin accounts'],
  ['普通管理员不能操作管理员账号', 'Regular admins cannot operate on admin accounts'],
  ['普通管理员不能重置管理员密码', 'Regular admins cannot reset an admin password'],
  ['登录失败次数过多，请稍后再试', 'Too many failed sign-in attempts. Try again later.'],
  ['新密码不能与当前密码相同', 'The new password must differ from the current password'],
  ['额度金额不能小于 0', 'Quota amount cannot be less than 0'],
  ['跨账户改绑 API KEY 需要提供完整密钥或超级管理员权限', 'Cross-account API key rebinding requires the full key or super admin access'],
  ['至少需要保留一个超级管理员', 'At least one super admin must remain'],
  ['不能禁用当前登录账号', 'You cannot disable the currently signed-in account'],
  ['密码过于简单，不能使用常见弱密码', 'Password is too weak and cannot be a common weak password'],
  ['密码修改失败次数过多，请稍后再试', 'Too many failed password-change attempts. Try again later.'],
  ['用户状态已变化，请刷新后重试', 'User state changed. Refresh and try again.'],
  ['禁用用户时用户状态已变化', 'User state changed while disabling the user.'],
  ['暂停用户额度时用户状态已变化', 'User state changed while pausing the quota.'],
  ['恢复额度用户时用户状态已变化', 'User state changed while restoring the quota.'],
  ['资源不存在', 'Resource does not exist'],
  ['渠道身份不完整', 'Channel identity is incomplete'],
  ['渠道名称不能包含控制字符或换行', 'Channel name cannot contain control characters or line breaks'],
  ['渠道名称不能超过 200 个字符', 'Channel name must not exceed 200 characters'],
  ['OpenAI-compatible 渠道名称请在渠道编辑页修改 Provider 名称', 'Change the Provider name in the channel editor to rename an OpenAI-compatible channel.'],
  ['渠道配置已变化，请刷新后重试', 'Channel configuration changed. Refresh and try again.'],
  ['渠道配置已变化，请刷新页面后重试', 'Channel configuration changed. Refresh and try again.'],
  ['请至少选择一个 LiteLLM 模型', 'Select at least one LiteLLM model'],
  ['所选 LiteLLM 模型包含重复定价身份，请只选择一个大小写或空格变体', 'Selected LiteLLM models share a price identity. Select only one case or whitespace variant.'],
  ['所选 LiteLLM 模型不存在或价格无效，请刷新清单后重试', 'A selected LiteLLM model is missing or has invalid pricing. Refresh the list and try again.'],
  ['使用明细已超过 7 天保留期', 'Usage record is outside the 7-day retention period'],
  ['请求明细仅保留最近 7 天，不能使用 range=all', 'Request records are retained for only the last 7 days; range=all is unavailable'],
  ['请求明细必须提供有效的时间范围', 'Request records require a valid time range'],
  ['请求明细时间范围无效', 'Request record time range is invalid'],
  ['请求明细仅保留最近 7 天', 'Request records are retained for only the last 7 days'],
  ['用户名或密码不正确', 'Username or password is incorrect'],
  ['系统尚未初始化，请先创建第一个管理员账号', 'The system is not initialized. Create the first admin account first.'],
  ['第一个管理员账号已存在', 'The first admin account already exists'],
  ['需要提供当前密码', 'Current password is required'],
  ['当前密码不正确', 'Current password is incorrect'],
  ['请先创建第一个管理员账号', 'Create the first admin account first'],
  ['尚未运行', 'Not run yet'],
  ['正在运行多个 Codex Keeper 任务', 'Multiple Codex Keeper tasks are running'],
  ['正在刷新 Codex 账号', 'Refreshing Codex accounts'],
  ['正在按条件刷新 Codex 账号', 'Refreshing Codex accounts by condition'],
  ['正在巡检 Codex 账号', 'Inspecting Codex accounts'],
  ['巡检完成', 'Inspection complete'],
  ['缓存时间内没有需要自动刷新的 Codex auth file', 'No Codex auth files need automatic refresh inside the cache window'],
  ['未发现指定 Codex auth file', 'No matching Codex auth file was found'],
  ['未发现 Codex auth file', 'No Codex auth files were found'],
  ['缺少 access token', 'Missing access token'],
  ['读取 auth file 详情失败', 'Failed to read auth file details'],
  ['管理密钥未设置，无法运行 Codex Keeper', 'Management key is not set, so Codex Keeper cannot run'],
  ['Codex Keeper 已开始按计划自动巡检', 'Codex Keeper scheduled automatic inspection started'],
  ['Codex Keeper 已停止自动巡检', 'Codex Keeper automatic inspection stopped'],
]

const serverCodeFallbackMessages = new Map<string, MessagePair>([
  ['app_error', ['服务器内部错误', 'Internal server error']],
  ['authentication_failed', ['请先登录', 'Sign in first']],
  ['conflict', ['请求冲突', 'Request conflict']],
  ['forbidden', ['需要管理员权限', 'Admin access is required']],
  ['method_not_allowed', ['请求方法不支持', 'Request method is not allowed']],
  ['not_found', ['资源不存在', 'Resource does not exist']],
  ['startup_check_failed', ['启动检查失败', 'Startup check failed']],
  ['usage_record_expired', ['使用明细已超过 7 天保留期', 'Usage record is outside the 7-day retention period']],
  ['validation_error', ['请求参数无效', 'Invalid request parameters']],
])

const serverTermTranslations: MessagePair[] = [
  ['CLIProxyAPI 管理请求', 'CLIProxyAPI management request'],
  ['CLIProxyAPI 管理', 'CLIProxyAPI management'],
  ['CLIProxyAPI 地址和管理密钥', 'CLIProxyAPI URL and management key'],
  ['CLIProxyAPI 地址', 'CLIProxyAPI URL'],
  ['管理密钥', 'management key'],
  ['Codex Keeper 自动启动配置', 'Codex Keeper auto-start settings'],
  ['Codex Keeper 自动巡检', 'Codex Keeper automatic inspection'],
  ['Codex Keeper 定时表达式', 'Codex Keeper schedule expression'],
  ['Codex Keeper 条件刷新配置', 'Codex Keeper conditional refresh settings'],
  ['Codex Keeper 条件刷新候选', 'Codex Keeper conditional refresh candidates'],
  ['Codex Keeper 配置', 'Codex Keeper settings'],
  ['Codex Keeper', 'Codex Keeper'],
  ['Codex auth file', 'Codex auth file'],
  ['Codex auth files', 'Codex auth files'],
  ['auth file 详情', 'auth file details'],
  ['auth files', 'auth files'],
  ['auth file', 'auth file'],
  ['CPA 配置', 'CPA settings'],
  ['CPA API KEY', 'CPA API key'],
  ['CPA 可用模型', 'CPA available models'],
  ['CPA 当前模型', 'CPA current models'],
  ['CPA 模型列表', 'CPA model list'],
  ['API KEY 标识', 'API key identifier'],
  ['API KEY 描述', 'API key description'],
  ['API KEY 绑定', 'API key binding'],
  ['API KEY', 'API key'],
  ['API Key', 'API key'],
  ['usage 记录', 'usage record'],
  ['usage 检测', 'usage check'],
  ['usage 开关', 'usage switch'],
  ['api-call 管理请求', 'api-call management request'],
  ['api-call 响应', 'api-call response'],
  ['模型请求地址', 'model request URL'],
  ['模型请求', 'model request'],
  ['模型响应', 'model response'],
  ['模型列表响应字段', 'model list response field'],
  ['模型列表响应', 'model list response'],
  ['模型列表', 'model list'],
  ['模型价格', 'model prices'],
  ['请求地址', 'request URL'],
  ['请求格式', 'request format'],
  ['请求体', 'request body'],
  ['测试模型名称', 'test model name'],
  ['测试模型', 'test model'],
  ['测试消息', 'test message'],
  ['远程 usage 开关', 'remote usage switch'],
  ['远程', 'remote'],
  ['代理地址', 'proxy URL'],
  ['价格数据', 'price data'],
  ['账号名称', 'account name'],
  ['账号类型', 'account type'],
  ['账号状态', 'account status'],
  ['账号巡检', 'account inspection'],
  ['账号和密码', 'account and password'],
  ['账号和昵称', 'account and nickname'],
  ['账号', 'account'],
  ['用户名', 'username'],
  ['昵称', 'nickname'],
  ['密码长度', 'password length'],
  ['密码', 'password'],
  ['第一个管理员账号', 'first admin account'],
  ['第一个用户', 'first user'],
  ['用户额度', 'user quota'],
  ['用户列表', 'users'],
  ['用户', 'user'],
  ['当前密码', 'current password'],
  ['登录会话', 'sign-in session'],
  ['完整密钥', 'full key'],
  ['原始 API KEY', 'original API key'],
  ['本地已不存在的 Codex 账号', 'local Codex accounts that no longer exist'],
  ['Codex 账号', 'Codex accounts'],
  ['坏凭证', 'bad credential'],
  ['凭证', 'credential'],
  ['网络检测', 'network check'],
  ['网络错误', 'network errors'],
  ['额度使用率', 'quota usage'],
  ['额度金额', 'quota amount'],
  ['额度', 'quota'],
  ['优先级降级', 'priority lowered'],
  ['优先级恢复', 'priority restored'],
  ['优先级', 'priority'],
  ['类型优先级', 'type priority'],
  ['巡检账号历史', 'account inspection history'],
  ['巡检', 'inspection'],
  ['条件刷新', 'conditional refresh'],
  ['账号刷新', 'account refresh'],
  ['刷新', 'refresh'],
  ['恢复启用', 'restored enabled state'],
  ['启用', 'enable'],
  ['禁用', 'disable'],
  ['缓存', 'cache'],
  ['健康', 'healthy'],
  ['跳过', 'skipped'],
  ['系统设置', 'system settings'],
  ['设置', 'settings'],
  ['账户', 'account'],
  ['可用模型', 'available models'],
  ['模型监控来源设置', 'model monitoring source settings'],
  ['模型监控来源配置', 'model monitoring source settings'],
  ['模型监控来源', 'model monitoring source'],
  ['代理配置', 'proxy settings'],
  ['API 密钥', 'API keys'],
  ['历史用量', 'usage history'],
  ['明细', 'records'],
  ['原始数据', 'raw data'],
  ['巡检配置', 'inspection settings'],
  ['正在运行', 'running'],
  ['正在其他 Keeper 任务处理中', 'being processed by another Keeper task'],
  ['无效', 'invalid'],
  ['不存在', 'does not exist'],
  ['已存在', 'already exists'],
  ['不能为空', 'is required'],
  ['不能超过', 'cannot exceed'],
  ['不能少于', 'must be at least'],
  ['不能小于', 'cannot be less than'],
  ['必须是', 'must be'],
  ['不是有效', 'is not valid'],
  ['格式不支持', 'format is unsupported'],
  ['响应缺少', 'response is missing'],
  ['超出范围', 'is out of range'],
  ['下载', 'download'],
  ['查询', 'query'],
  ['读取', 'read'],
  ['写入', 'write'],
  ['保存', 'save'],
  ['删除', 'delete'],
  ['同步', 'sync'],
  ['更新', 'update'],
  ['创建', 'create'],
  ['失败', 'failed'],
  ['成功', 'succeeded'],
  ['完成', 'complete'],
  ['：', ': '],
  ['，', ', '],
  ['；', '; '],
  ['。', '.'],
]

const modelMonitorSubjectTranslations: MessagePair[] = [
  ['可见结构引用的 component', 'component referenced by the visible structure'],
  ['页面分组', 'page group'],
  ['当前组件', 'current component'],
  ['分钟样本', 'minute sample'],
  ['历史元数据', 'history metadata'],
  ['impact 时间范围', 'impact time range'],
  ['uptime group 引用', 'uptime group reference'],
  ['赋值尾部', 'assignment suffix'],
  ['当前状态', 'current status'],
  ['未知组件', 'unknown component'],
  ['必要字段', 'required fields'],
  ['非空对象', 'non-empty object'],
  ['组件', 'component'],
  ['页面', 'page'],
  ['响应', 'response'],
  ['字段', 'field'],
  ['数组', 'array'],
  ['历史', 'history'],
  ['日期', 'date'],
  ['名称', 'name'],
  ['标题', 'title'],
]

const modelMonitorTransportMessagePatterns: ServerMessagePattern[] = [
  [/^模型监控请求失败:\s*(.+)$/, ([, detail]) => `Model monitoring request failed: ${translateModelMonitorRequestDetail(detail ?? '')}`],
  [/^模型监控上游返回 HTTP (\d+)$/, ([, status]) => `Model monitoring upstream returned HTTP ${status}`],
  [/^模型监控上游返回了不支持的内容类型 (.+)$/, ([, contentType]) => `Model monitoring upstream returned unsupported content type ${contentType}`],
  [/^读取模型监控上游响应失败:\s*(.+)$/, ([, detail]) => `Failed to read model monitoring upstream response: ${detail}`],
  [/^模型监控上游响应超过 (.+) 限制$/, ([, limit]) => `Model monitoring upstream response exceeds the ${limit} limit`],
]

// The AI radar proxies a third-party feed, so its failures never reach the
// client as HTTP errors; these patterns localize the reason it reports.
const aiRadarTransportMessagePatterns: ServerMessagePattern[] = [
  [/^AI 雷达请求失败:\s*(.+)$/, ([, detail]) => `AI radar request failed: ${translateAIRadarRequestDetail(detail ?? '')}`],
  [/^AI 雷达上游返回 HTTP (\d+)$/, ([, status]) => `AI radar upstream returned HTTP ${status}`],
  [/^AI 雷达上游返回了不支持的内容类型 (.+)$/, ([, contentType]) => `AI radar upstream returned unsupported content type ${contentType}`],
  [/^读取 AI 雷达上游响应失败:\s*(.+)$/, ([, detail]) => `Failed to read the AI radar upstream response: ${detail}`],
  [/^AI 雷达上游响应超过 (.+) 限制$/, ([, limit]) => `AI radar upstream response exceeds the ${limit} limit`],
  [/^AI 雷达上游返回了无效 JSON:\s*(.+)$/, ([, detail]) => `AI radar upstream returned invalid JSON: ${detail}`],
  [/^AI 雷达上游 schema 不受支持:\s*(-?\d+)$/, ([, version]) => `AI radar upstream schema ${version} is not supported`],
  [/^AI 雷达上游缺少 points 数组$/, () => 'AI radar upstream is missing the points array'],
  [/^AI 雷达上游 points 不是数组:\s*(.+)$/, ([, detail]) => `AI radar upstream points is not an array: ${detail}`],
  [/^AI 雷达上游缺少有效 source_updated_at$/, () => 'AI radar upstream is missing a valid source_updated_at'],
  [/^AI 雷达时间范围无效:\s*(.*)$/, ([, value]) => `AI radar time range is invalid: ${value}`],
  [/^AI 雷达历史快照不是有效 JSON:\s*(.+)$/, ([, detail]) => `AI radar history snapshot is not valid JSON: ${detail}`],
  [/^AI 雷达历史快照没有可导入的 GPT 观测点$/, () => 'AI radar history snapshot has no importable GPT observations'],
]

// Preserve opaque upstream identifiers, including when UI errorText localizes an API error again.
const pluginDiagnosticMessagePatterns: ServerMessagePattern[] = [
  [/^(?:CPA 上游配置格式错误|Invalid CPA upstream configuration format): (.+)$/, ([, field]) => `Invalid CPA upstream configuration format: ${field}`],
  [/^(?:CPA 上游配置条目格式错误|Invalid CPA upstream configuration entry format): (.+)$/, ([, field]) => `Invalid CPA upstream configuration entry format: ${field}`],
  [/^(?:CPA 上游分组格式错误|Invalid CPA upstream group format): (.+)$/, ([, provider]) => `Invalid CPA upstream group format: ${provider}`],
  [/^(?:CPA 上游凭据格式错误|Invalid CPA upstream credential format): (.+)$/, ([, provider]) => `Invalid CPA upstream credential format: ${provider}`],
  [/^(?:CPA 上游类型不受支持|Unsupported CPA upstream provider type): (.+)$/, ([, provider]) => `Unsupported CPA upstream provider type: ${provider}`],
]

const serverMessagePatterns: ServerMessagePattern[] = [
  [/^CLIProxyAPI 管理地址无效：(.+)$/, ([, detail]) => `Invalid CLIProxyAPI management URL: ${translateNested(detail ?? '')}`],
  [/^查询 CPA 模型目录失败：(.+)$/, ([, detail]) => `Failed to query CPA model catalog: ${translateNested(detail ?? '')}`],
  [/^CPA 模型列表请求失败：(.+)$/, ([, detail]) => `CPA model list request failed: ${translateNested(detail ?? '')}`],
  [/^CPA 模型列表响应字段 (.+) 不是列表$/, ([, field]) => `CPA model list response field ${field} is not a list`],
  [/^CPA Helper Plugin 请求失败（上游 HTTP (\d+)）$/, ([, status]) => `CPA Helper Plugin request failed (upstream HTTP ${status})`],
  [/^CPA Helper Plugin 暂不可用（上游 HTTP (\d+)）$/, ([, status]) => `CPA Helper Plugin is temporarily unavailable (upstream HTTP ${status})`],
  [/^峰谷规则缺少字段: (.+)$/, (match) => `Time pricing rule is missing field: ${match[1]}`],
  [/^空闲价格缺少字段: (.+)$/, (match) => `Off-peak rates are missing field: ${match[1]}`],
  [/^单次最多批量应用 (\d+) 个渠道模型价格$/, ([, count]) => `A maximum of ${count} channel model prices can be applied at once`],
  [/^操作失败$/, () => 'Operation failed'],
  [/^模型监控来源配置无效:\s*(.+)$/, ([, detail]) => `Model monitoring source settings are invalid: ${translateModelMonitorSourceSettingsDetail(detail ?? '')}`],
  [/^模型监控来源设置缺少 enabled_source_ids 字段$/, () => 'Model monitoring source settings are missing enabled_source_ids'],
  [/^enabled_source_ids 必须是 JSON 字符串数组$/, () => 'enabled_source_ids must be a JSON string array'],
  [/^模型监控来源 ID 不能为空$/, () => 'Model monitoring source ID is required'],
  [/^模型监控来源 ID "(.+)" 不受支持$/, ([, id]) => `Model monitoring source ID "${id}" is not supported`],
  [/^模型监控来源 ID "(.+)" 重复$/, ([, id]) => `Model monitoring source ID "${id}" is duplicated`],
  [/^模型监控来源不存在$/, () => 'Model monitoring source does not exist'],
  [/^(OpenAI|Anthropic|DeepSeek|AI\.INPUT\.IM)(?: (.+?))? 响应不是有效 JSON(?::\s*(.+))?$/, ([, source, subject, detail]) => `${source}${subject ? ` ${translateModelMonitorSubject(subject)}` : ''} response is not valid JSON${detail ? `: ${detail}` : ''}`],
  [/^(OpenAI|Anthropic|DeepSeek|AI\.INPUT\.IM) (.+?)响应缺少\s*(.+)$/, ([, source, subject, fields]) => `${source} ${translateModelMonitorSubject(subject)} response is missing ${translateModelMonitorSubject(fields ?? '')}`],
  [/^(OpenAI|Anthropic|DeepSeek|AI\.INPUT\.IM) (.+)缺少\s*必要字段$/, ([, source, subject]) => `${source} ${translateModelMonitorSubject(subject)} is missing required fields`],
  [/^(OpenAI|Anthropic|DeepSeek|AI\.INPUT\.IM) (.+)结构无效$/, ([, source, subject]) => `${source} ${translateModelMonitorSubject(subject)} structure is invalid`],
  [/^(OpenAI|Anthropic|DeepSeek|AI\.INPUT\.IM) (.+)字段无效$/, ([, source, subject]) => `${source} ${translateModelMonitorSubject(subject)} field is invalid`],
  [/^(OpenAI|Anthropic|DeepSeek|AI\.INPUT\.IM) (.+)不是有效的非空对象$/, ([, source, subject]) => `${source} ${translateModelMonitorSubject(subject)} is not a valid non-empty object`],
  [/^(OpenAI|Anthropic|DeepSeek|AI\.INPUT\.IM) (.+)为空$/, ([, source, subject]) => `${source} ${translateModelMonitorSubject(subject)} is empty`],
  [/^(OpenAI|Anthropic|DeepSeek|AI\.INPUT\.IM) (.+)重复$/, ([, source, subject]) => `${source} ${translateModelMonitorSubject(subject)} is duplicated`],
  [/^(OpenAI|Anthropic|DeepSeek|AI\.INPUT\.IM) (.+)无效(?::\s*(.+))?$/, ([, source, subject, detail]) => `${source} ${translateModelMonitorSubject(subject)} is invalid${detail ? `: ${translateModelMonitorDetail(detail)}` : ''}`],
  [/^(OpenAI|Anthropic|DeepSeek|AI\.INPUT\.IM) (.+?)缺少\s*(.+)$/, ([, source, subject, missing]) => `${source} ${translateModelMonitorSubject(subject)} is missing ${translateModelMonitorSubject(missing)}`],
  [/^(OpenAI|Anthropic|DeepSeek|AI\.INPUT\.IM) (.+)包含未知组件 (.+)$/, ([, source, subject, component]) => `${source} ${translateModelMonitorSubject(subject)} contains unknown component ${component}`],
  [/^(OpenAI|Anthropic|DeepSeek|AI\.INPUT\.IM) (.+)包含无效日期$/, ([, source, subject]) => `${source} ${translateModelMonitorSubject(subject)} contains an invalid date`],
  [/^OpenAI summary 引用了 components 响应中不存在的组件 (.+)$/, ([, component]) => `OpenAI summary references component ${component} that is absent from the components response`],
  [/^OpenAI (.+)引用了未知\s*(.+)$/, ([, subject, target]) => `OpenAI ${translateModelMonitorSubject(subject)} references unknown ${translateModelMonitorSubject(target)}`],
  [/^OpenAI (.+)引用了无法映射的 incident (.+)$/, ([, subject, incident]) => `OpenAI ${translateModelMonitorSubject(subject)} references unmapped incident ${incident}`],
  [/^OpenAI 当前 component (.+) 未出现在页面分组中$/, ([, component]) => `OpenAI current component ${component} does not appear in the page groups`],
  [/^OpenAI 可见 group (.+) 没有可见 component$/, ([, group]) => `OpenAI visible group ${group} has no visible component`],
  [/^OpenAI 页面当前结构数量为 (\d+)、历史结构数量为 (\d+)，均期望 (\d+)$/, ([, current, history, expected]) => `OpenAI page has ${current} current structures and ${history} history structures; expected ${expected} of each`],
  [/^(Anthropic|DeepSeek) 页面 uptimeData 赋值数量为 (\d+)，期望 (\d+)$/, ([, source, actual, expected]) => `${source} page uptimeData assignment count is ${actual}; expected ${expected}`],
  [/^(OpenAI|Anthropic|DeepSeek|AI\.INPUT\.IM) (.+)不一致$/, ([, source, subject]) => `${source} ${translateModelMonitorSubject(subject)} does not match`],
  [/^加载(.+)失败$/, ([, subject]) => `Failed to load ${translateTerms(subject ?? '')}`],
  [/^保存(.+)失败$/, ([, subject]) => `Failed to save ${translateTerms(subject ?? '')}`],
  [/^删除(.+)失败$/, ([, subject]) => `Failed to delete ${translateTerms(subject ?? '')}`],
  [/^同步(.+)失败$/, ([, subject]) => `Failed to sync ${translateTerms(subject ?? '')}`],
  [/^更新(.+)失败$/, ([, subject]) => `Failed to update ${translateTerms(subject ?? '')}`],
  [/^创建(.+)失败$/, ([, subject]) => `Failed to create ${translateTerms(subject ?? '')}`],
  [/^(.+)和(.+)不能为空$/, ([, left, right]) => `${translateTerms(left ?? '')} and ${translateTerms(right ?? '')} are required`],
  [/^(.+)或(.+)不能为空$/, ([, left, right]) => `${translateTerms(left ?? '')} or ${translateTerms(right ?? '')} is required`],
  [/^(.+)不能为空$/, ([, subject]) => `${translateTerms(subject ?? '')} is required`],
  [/^(.+)不能超过 (\d+) 个字符$/, ([, subject, count]) => `${translateTerms(subject ?? '')} cannot exceed ${count} characters`],
  [/^(.+)不能少于 (\d+) 位$/, ([, subject, count]) => `${translateTerms(subject ?? '')} must be at least ${count} characters`],
  [/^(.+)不能小于 (\d+)$/, ([, subject, count]) => `${translateTerms(subject ?? '')} cannot be less than ${count}`],
  [/^(.+)超出范围$/, ([, subject]) => `${translateTerms(subject ?? '')} is out of range`],
  [/^(.+)无效$/, ([, subject]) => `${translateTerms(subject ?? '')} is invalid`],
  [/^(.+)不存在$/, ([, subject]) => `${translateTerms(subject ?? '')} does not exist`],
  [/^(.+)已存在$/, ([, subject]) => `${translateTerms(subject ?? '')} already exists`],
  [/^(.+)不是有效 JSON$/, ([, subject]) => `${translateTerms(subject ?? '')} is not valid JSON`],
  [/^(.+)响应不是有效 JSON$/, ([, subject]) => `${translateTerms(subject ?? '')} response is not valid JSON`],
  [/^(.+)响应格式不支持$/, ([, subject]) => `${translateTerms(subject ?? '')} response format is unsupported`],
  [/^(.+)响应缺少(.+)$/, ([, subject, field]) => `${translateTerms(subject ?? '')} response is missing ${translateTerms(field ?? '')}`],
  [/^(.+)请求失败：HTTP (\d+)$/, ([, subject, status]) => `${translateTerms(subject ?? '')} request failed: HTTP ${status}`],
  [/^(.+)请求失败：(.+)$/, ([, subject, detail]) => `${translateTerms(subject ?? '')} request failed: ${translateNested(detail ?? '')}`],
  [/^(.+)失败：HTTP (\d+)：(.+)$/, ([, subject, status, detail]) => `${translateTerms(subject ?? '')} failed: HTTP ${status}: ${translateNested(detail ?? '')}`],
  [/^(.+)失败：HTTP (\d+)$/, ([, subject, status]) => `${translateTerms(subject ?? '')} failed: HTTP ${status}`],
  [/^(.+)失败：(.+)$/, ([, subject, detail]) => `${translateTerms(subject ?? '')} failed: ${translateNested(detail ?? '')}`],
  [/^(.+)失败$/, ([, subject]) => `${translateTerms(subject ?? '')} failed`],
  [/^(.+)超时，请检查(.+)$/, ([, subject, detail]) => `${translateTerms(subject ?? '')} timed out. Check ${translateTerms(detail ?? '')}`],
  [/^URL 必须是有效的 HTTP\/HTTPS 地址$/, () => 'URL must be a valid HTTP/HTTPS URL'],
  [/^(.+)必须是有效的 http:\/\/ 或 https:\/\/ 地址$/, ([, subject]) => `${translateTerms(subject ?? '')} must be a valid http:// or https:// URL`],
  [/^(.+)必须使用 http:\/\/ 或 https:\/\/$/, ([, subject]) => `${translateTerms(subject ?? '')} must use http:// or https://`],
  [/^(.+)不能包含查询参数或锚点$/, ([, subject]) => `${translateTerms(subject ?? '')} cannot contain query parameters or anchors`],
  [/^(.+)必须是有效的 http:\/\/、https:\/\/ 或 socks5:\/\/ 地址$/, ([, subject]) => `${translateTerms(subject ?? '')} must be a valid http://, https://, or socks5:// URL`],
  [/^当前 (.+) 缺少完整密钥，无法发起测试$/, ([, subject]) => `The current ${translateTerms(subject ?? '')} is missing the full key and cannot start a test`],
  [/^用户额度已用尽，API KEY 已暂停，请联系管理员补充额度$/, () => 'User quota is exhausted. API keys are paused; contact an admin to add quota.'],
  [/^用户额度已用尽，请补充额度后再恢复 API KEY$/, () => 'User quota is exhausted. Add quota before restoring API keys.'],
  [/^存在无法恢复的 API KEY，请重新绑定后再启用$/, () => 'Some API keys cannot be restored. Rebind them before enabling the user.'],
  [/^存在无法恢复的 API KEY，请重新绑定后再恢复$/, () => 'Some API keys cannot be restored. Rebind them before restoring.'],
  [/^API KEY 与 API KEY 标识不匹配$/, () => 'API key does not match the API key identifier'],
  [/^API KEY 或 API KEY 标识不能为空$/, () => 'API key or API key identifier is required'],
  [/^未找到完整 API KEY，请粘贴原始 API KEY$/, () => 'Full API key was not found. Paste the original API key.'],
  [/^生成 API KEY 失败，请重试$/, () => 'Failed to generate API key. Try again.'],
  [/^第一个管理员账号不能禁用$/, () => 'The first admin account cannot be disabled'],
  [/^账号不允许修改$/, () => 'Account cannot be changed'],
  [/^第一个管理员账号不能取消管理员权限$/, () => 'The first admin account cannot lose admin access'],
  [/^用户已禁用$/, () => 'User is disabled'],
  [/^当前 API KEY 缺少完整密钥，无法发起测试$/, () => 'The current API key is missing the full key and cannot start a test'],
  [/^模型请求失败：HTTP (\d+)：(.+)$/, ([, status, detail]) => `Model request failed: HTTP ${status}: ${translateNested(detail ?? '')}`],
  [/^模型请求失败：HTTP (\d+)$/, ([, status]) => `Model request failed: HTTP ${status}`],
  [/^模型请求失败：(.+)$/, ([, detail]) => `Model request failed: ${translateNested(detail ?? '')}`],
  [/^模型请求超时，请检查模型请求地址或稍后重试$/, () => 'Model request timed out. Check the model request URL or try again later.'],
  [/^模型响应不是有效 JSON$/, () => 'Model response is not valid JSON'],
  [/^请求格式不支持$/, () => 'Request format is unsupported'],
  [/^查询 CPA 可用模型失败：(.+)$/, ([, detail]) => `Failed to query CPA available models: ${translateNested(detail ?? '')}`],
  [/^下载 LiteLLM 价格数据失败：HTTP (\d+)$/, ([, status]) => `Failed to download LiteLLM price data: HTTP ${status}`],
  [/^下载 LiteLLM 价格数据失败$/, () => 'Failed to download LiteLLM price data'],
  [/^LiteLLM 价格数据不是有效 JSON$/, () => 'LiteLLM price data is not valid JSON'],
  [/^该 provider\/model 价格已存在$/, () => 'This provider/model price already exists'],
  [/^provider\/model 不能为空$/, () => 'Provider/model is required'],
  [/^价格不能为负数$/, () => 'Price cannot be negative'],
  [/^启用代理时必须填写代理地址$/, () => 'Proxy URL is required when proxy is enabled'],
  [/^Cron 表达式无效，请使用 5 段格式：分 时 日 月 周$/, () => 'Invalid Cron expression. Use the 5-field format: minute hour day month weekday'],
  [/^Cron 表达式无效，请使用 5 段格式$/, () => 'Invalid Cron expression. Use the 5-field format'],
  [/^开始按条件刷新 (\d+) 个 Codex 账号$/, ([, count]) => `Started conditional refresh for ${count} Codex accounts`],
  [/^开始刷新 (\d+) 个 Codex 账号$/, ([, count]) => `Started refreshing ${count} Codex accounts`],
  [/^开始 Codex 账号巡检$/, () => 'Started Codex account inspection'],
  [/^下一轮计划：(.+)$/, ([, time]) => `Next scheduled run: ${time}`],
  [/^清理本地已不存在的 Codex 账号 (\d+) 个$/, ([, count]) => `Cleaned up ${count} local Codex accounts that no longer exist`],
  [/^巡检完成：网络错误 (\d+)$/, ([, count]) => `Inspection complete: ${count} network errors`],
  [/^条件刷新完成：健康 (\d+)，坏凭证禁用 (\d+)，恢复启用 (\d+)，优先级降级 (\d+)，优先级恢复 (\d+)，网络错误 (\d+)，缓存跳过 (\d+)$/, ([, healthy, disabled, restored, degraded, priorityRestored, networkErrors, skipped]) => `Conditional refresh complete: ${healthy} healthy, ${disabled} bad credentials disabled, ${restored} restored, ${degraded} priorities lowered, ${priorityRestored} priorities restored, ${networkErrors} network errors, ${skipped} skipped by cache`],
  [/^账号刷新完成：健康 (\d+)，凭证异常 (\d+)，恢复启用 (\d+)，优先级降级 (\d+)，优先级恢复 (\d+)，网络错误 (\d+)$/, ([, healthy, credentialErrors, restored, degraded, priorityRestored, networkErrors]) => `Account refresh complete: ${healthy} healthy, ${credentialErrors} credential errors, ${restored} restored, ${degraded} priorities lowered, ${priorityRestored} priorities restored, ${networkErrors} network errors`],
  [/^巡检完成：健康 (\d+)，坏凭证禁用 (\d+)，恢复启用 (\d+)，优先级降级 (\d+)，网络错误 (\d+)，缓存跳过 (\d+)$/, ([, healthy, disabled, restored, degraded, networkErrors, skipped]) => `Inspection complete: ${healthy} healthy, ${disabled} bad credentials disabled, ${restored} restored, ${degraded} priorities lowered, ${networkErrors} network errors, ${skipped} skipped by cache`],
  [/^(.+): 巡检正常，类型 (.+)$/, ([, name, accountType]) => `${name}: inspection healthy, type ${accountType}`],
  [/^(.+): 正在其他 Keeper 任务处理中，跳过$/, ([, name]) => `${name}: skipped because another Keeper task is processing it`],
  [/^(.+): 缓存时间内已刷新，跳过$/, ([, name]) => `${name}: skipped because it was refreshed inside the cache window`],
  [/^刷新完成，类型 (.+)$/, ([, accountType]) => `Refresh complete, type ${accountType}`],
  [/^禁用凭证：(.+)$/, ([, detail]) => `Disabled credential: ${translateNested(detail ?? '')}`],
  [/^模拟禁用：(.+)$/, ([, detail]) => `Dry run disable: ${translateNested(detail ?? '')}`],
  [/^刷新发现凭证不可用：(.+)$/, ([, detail]) => `Refresh found unavailable credential: ${translateNested(detail ?? '')}`],
  [/^凭证不可用：HTTP (\d+)$/, ([, status]) => `Credential unavailable: HTTP ${status}`],
  [/^网络检测失败：(.+)$/, ([, detail]) => `Network check failed: ${translateNested(detail ?? '')}`],
  [/^usage 检测失败：HTTP (\d+)$/, ([, status]) => `Usage check failed: HTTP ${status}`],
  [/^恢复启用：usage 检测恢复 HTTP (\d+)$/, ([, status]) => `Restored enabled state: usage check recovered with HTTP ${status}`],
  [/^模拟恢复启用：usage 检测恢复 HTTP (\d+)$/, ([, status]) => `Dry run restore enabled state: usage check recovered with HTTP ${status}`],
  [/^降为低优先级：额度使用率达到阈值 (\d+)%$/, ([, percent]) => `Lowered priority: quota usage reached the ${percent}% threshold`],
  [/^模拟降为低优先级：额度使用率达到阈值 (\d+)%$/, ([, percent]) => `Dry run lower priority: quota usage reached the ${percent}% threshold`],
  [/^恢复优先级：priority (-?\d+)$/, ([, priority]) => `Restored priority: priority ${priority}`],
  [/^模拟恢复优先级：priority (-?\d+)$/, ([, priority]) => `Dry run restore priority: priority ${priority}`],
  [/^应用类型优先级：(.+) -> priority (-?\d+)$/, ([, accountType, priority]) => `Applied type priority: ${accountType} -> priority ${priority}`],
  [/^模拟应用类型优先级：(.+) -> priority (-?\d+)$/, ([, accountType, priority]) => `Dry run apply type priority: ${accountType} -> priority ${priority}`],
  [/^启用 WebSocket 传输失败：(.+)$/, ([, detail]) => `Failed to enable WebSocket transport: ${translateNested(detail ?? '')}`],
  [/^(.+): 已启用 WebSocket 传输$/, ([, name]) => `${name}: enabled WebSocket transport`],
  [/^只能删除已禁用账号$/, () => 'Only disabled accounts can be deleted'],
  [/^只能设置小于 -1、大于 20，或当前账号类型 (.+) 对应的 priority (-?\d+)$/, ([, accountType, priority]) => `Priority must be less than -1, greater than 20, or the current account type ${accountType} priority ${priority}`],
  [/^该账号类型没有可设置的系统 priority$/, () => 'This account type has no system priority that can be set'],
  [/^模拟(.+)$/, ([, action]) => `Dry run: ${translateNested(action ?? '')}`],
  [/^(.+?): (.+)$/, ([, prefix, detail]) => `${prefix}: ${translateNested(detail ?? '')}`],
]

function containsHan(value: string): boolean {
  return /\p{Script=Han}/u.test(value)
}

function translateTerms(value: string): string {
  return serverTermTranslations.reduce(
    (text, [zh, en]) => text.split(zh).join(en),
    value,
  )
}

function translateModelMonitorSubject(value: string | undefined): string {
  return (value ?? '')
    .split(/("(?:\\.|[^"\\])*")/g)
    .map((part, index) => {
      if (index % 2 === 1) return part
      return modelMonitorSubjectTranslations.reduce(
        (text, [zh, en]) => text.split(zh).join(en),
        part,
      )
    })
    .join('')
    .replace(/\s*的\s*/g, ' ')
    .replace(/\s*或\s*/g, ' or ')
    .replace(/\s+/g, ' ')
    .trim()
}

function translateModelMonitorDetail(value: string): string {
  switch (value) {
    case '赋值右侧不是 JSON 对象':
      return 'assignment value is not a JSON object'
    case 'JSON 对象未闭合':
      return 'JSON object is not closed'
    default:
      return value
  }
}

function translateModelMonitorSourceSettingsDetail(value: string): string {
  switch (value) {
    case 'enabled_source_ids 必须是 JSON 字符串数组':
      return 'enabled_source_ids must be a JSON string array'
    case '模型监控来源 ID 不能为空':
      return 'model monitoring source ID is required'
  }

  const unsupported = value.match(/^模型监控来源 ID "(.+)" 不受支持$/)
  if (unsupported) return `model monitoring source ID "${unsupported[1]}" is not supported`

  const duplicate = value.match(/^模型监控来源 ID "(.+)" 重复$/)
  if (duplicate) return `model monitoring source ID "${duplicate[1]}" is duplicated`

  return value
}

function translateModelMonitorRequestDetail(value: string): string {
  const fixedDetails: MessagePair[] = [
    ['状态页重定向必须使用 HTTPS', 'Status page redirects must use HTTPS'],
    ['状态页重定向次数过多', 'Too many status page redirects'],
  ]
  for (const [zh, en] of fixedDetails) {
    if (value === zh) return en
    const wrappedSuffix = `: ${zh}`
    if (value.endsWith(wrappedSuffix)) {
      return `${value.slice(0, -zh.length)}${en}`
    }
  }
  return value
}

function translateModelMonitorTransportMessage(message: string): string | null {
  for (const [pattern, translate] of modelMonitorTransportMessagePatterns) {
    const match = message.match(pattern)
    if (match) return translate(match)
  }
  return null
}

function translateAIRadarRequestDetail(value: string): string {
  const fixedDetails: MessagePair[] = [
    ['AI 雷达重定向必须使用 HTTPS', 'AI radar redirects must use HTTPS'],
    ['AI 雷达重定向次数过多', 'Too many AI radar redirects'],
  ]
  for (const [zh, en] of fixedDetails) {
    if (value === zh) return en
    const wrappedSuffix = `: ${zh}`
    if (value.endsWith(wrappedSuffix)) {
      return `${value.slice(0, -zh.length)}${en}`
    }
  }
  return value
}

function translateAIRadarTransportMessage(message: string): string | null {
  for (const [pattern, translate] of aiRadarTransportMessagePatterns) {
    const match = message.match(pattern)
    if (match) return translate(match)
  }
  return null
}

function translateNested(value: string): string {
  return translateKnownServerMessage(value) ?? translateTerms(value)
}

function translateKnownServerMessage(message: string): string | null {
  const exact = exactServerMessages.find(([zh]) => zh === message)
  if (exact) {
    return exact[1]
  }

  for (const [pattern, translate] of serverMessagePatterns) {
    const match = message.match(pattern)
    if (match) {
      return translate(match)
    }
  }

  const termTranslated = translateTerms(message)
  if (termTranslated !== message) {
    return termTranslated
  }

  return null
}

export function localizedServerMessage(
  message: string,
  fallbackZh = '请求失败',
  fallbackEn = 'Request failed',
): string {
  if (currentLanguage.value === 'zh') {
    return message || fallbackZh
  }

  if (!message) {
    return fallbackEn
  }

  for (const [pattern, translate] of pluginDiagnosticMessagePatterns) {
    const match = message.match(pattern)
    if (match) return translate(match)
  }

  const modelMonitorTransportMessage = translateModelMonitorTransportMessage(message)
  if (modelMonitorTransportMessage) {
    return modelMonitorTransportMessage
  }

  const aiRadarTransportMessage = translateAIRadarTransportMessage(message)
  if (aiRadarTransportMessage) {
    return aiRadarTransportMessage
  }

  if (!containsHan(message)) {
    return message
  }

  const translated = translateKnownServerMessage(message)
  if (translated) {
    return containsHan(translated) ? `${fallbackEn}: ${translated}` : translated
  }

  return `${fallbackEn}: ${message}`
}

export function localizedApiErrorMessage(
  code: string | null | undefined,
  message: string | null | undefined,
): string {
  const fallback = code ? serverCodeFallbackMessages.get(code) : null
  if (message) {
    return localizedServerMessage(
      message,
      fallback?.[0] ?? '请求失败',
      fallback?.[1] ?? 'Request failed',
    )
  }
  if (fallback) {
    return localizedServerMessage('', fallback[0], fallback[1])
  }
  return localizedServerMessage('', '请求失败', 'Request failed')
}

export function localizedKeeperStatusDetail(message: string | null | undefined): string {
  if (!message) {
    return localize('未运行', 'Not running')
  }
  const normalized = message
    .replace(/守护运行中/g, localize('自动巡检运行中', 'Automatic inspection running'))
    .replace(/守护进程/g, localize('后台自动巡检', 'background automatic inspection'))
    .replace(/守护任务/g, localize('自动巡检任务', 'automatic inspection task'))
    .replace(/守护已启动/g, localize('已开始自动巡检', 'Automatic inspection started'))
  return currentLanguage.value === 'zh'
    ? normalized
    : localizedServerMessage(normalized, '运行状态', 'Run status')
}

function channelBrandLabels(brand: string): readonly [zh: string, en: string] {
  switch (brand) {
    case 'gemini':
      return ['Gemini', 'Gemini']
    case 'codex':
      return ['Codex', 'Codex']
    case 'claude':
      return ['Claude', 'Claude']
    case 'vertex':
      return ['Vertex', 'Vertex']
    case 'xai':
      return ['xAI', 'xAI']
    case 'openai_compatibility':
      return ['OpenAI 兼容', 'OpenAI-compatible']
    default:
      return ['未知渠道', 'Unknown channel']
  }
}

export function localizedUsageChannelFallbackLabel(brand: string, authType: string): string {
  const [brandZh, brandEn] = channelBrandLabels(brand)
  if (authType === 'oauth') {
    return localize(`${brandZh} OAuth（标签不可用）`, `${brandEn} OAuth (label unavailable)`)
  }
  if (brand === 'openai_compatibility') {
    return localize(`${brandZh}渠道（标签不可用）`, `${brandEn} channel (label unavailable)`)
  }
  return localize(`${brandZh} API Key（标签不可用）`, `${brandEn} API Key (label unavailable)`)
}

export function errorText(
  error: unknown,
  fallbackZh = '请求失败',
  fallbackEn = 'Request failed',
): string {
  const message = error instanceof Error ? error.message : ''
  return localizedServerMessage(message, fallbackZh, fallbackEn)
}

export function copiedText(label: string): string {
  return localize(`${label} 已复制`, `${label} copied`)
}
