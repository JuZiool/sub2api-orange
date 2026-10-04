//! API Key 的创建 / 单查 / 删除逻辑。
//!
//! 对齐 Go 版：
//! - `internal/handler/api_key_handler.go` 的 `Create` / `GetByID` / `Delete`
//! - `internal/service/api_key_service.go` 的 `Create` / `GenerateKey` /
//!   `ValidateCustomKey` / `validateAPIKeyFallbackGroup` / `Delete`
//! - `internal/repository/api_key_repo.go` 的 `DeleteWithAudit`（tombstone）
//!
//! ## 三个易错点
//!
//! 1. **名称会被 HTML 转义**：Go 用 `html.EscapeString(req.Name)` 后入库。
//! 2. **删除是 tombstone**：不仅设 `deleted_at`，还会把 `key` 列改写为
//!    `__deleted__{id}__{纳秒}`，释放唯一键占用。
//! 3. **分组绑定有权限校验**：公开分组默认可绑，专属分组与受限用户需在
//!    `allowed_groups` 内；订阅型分组需有效订阅。

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};

use crate::handler::api_key::ApiKeyRecord;

/// 自定义 key 最小长度，与 Go 版 `ErrAPIKeyTooShort` 一致。
pub const CUSTOM_KEY_MIN_LEN: usize = 16;

/// 默认 key 前缀，与 Go 版 `Default.APIKeyPrefix` 缺省值一致。
pub const DEFAULT_API_KEY_PREFIX: &str = "sk-";

/// 创建 API Key 请求，字段名对齐 Go 版 `CreateAPIKeyRequest`。
#[derive(Debug, Clone, Deserialize, Default)]
pub struct CreateApiKeyRequest {
    /// 名称。Go 的 `binding:"required"`，但空串与缺失不同：
    /// 缺失会让 gin 绑定报错；这里用 `Option` 区分。
    #[serde(default)]
    pub name: Option<String>,
    #[serde(default)]
    pub group_id: Option<i64>,
    #[serde(default)]
    pub fallback_group_id: Option<i64>,
    /// 自定义 key（可选）。
    #[serde(default)]
    pub custom_key: Option<String>,
    #[serde(default)]
    pub ip_whitelist: Vec<String>,
    #[serde(default)]
    pub ip_blacklist: Vec<String>,
    #[serde(default)]
    pub quota: Option<f64>,
    #[serde(default)]
    pub expires_in_days: Option<i64>,
    #[serde(default)]
    pub rate_limit_5h: Option<f64>,
    #[serde(default)]
    pub rate_limit_1d: Option<f64>,
    #[serde(default)]
    pub rate_limit_7d: Option<f64>,
}

/// 创建请求校验失败原因，对应 Go 版各错误码。
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum CreateValidationError {
    /// 名称为空（`binding:"required"`）。
    NameRequired,
    /// 数值限额非法（NaN/Inf/负数）。
    InvalidLimit(&'static str),
    /// `expires_in_days` 非正。
    InvalidExpiry,
}

impl CreateValidationError {
    /// 对应 Go 版的 reason 与文案。
    pub fn code_and_message(&self) -> (&'static str, String) {
        match self {
            CreateValidationError::NameRequired => (
                "INVALID_REQUEST",
                "Invalid request: name is required".to_string(),
            ),
            CreateValidationError::InvalidLimit(field) => (
                "API_KEY_LIMIT_INVALID",
                format!("Invalid request: {field} must be finite and non-negative"),
            ),
            CreateValidationError::InvalidExpiry => (
                "API_KEY_EXPIRY_INVALID",
                "Invalid request: expires_in_days must be greater than zero".to_string(),
            ),
        }
    }
}

/// 限额是否合法：有限且非负，对应 Go 的 `validAPIKeyLimit`。
pub fn valid_limit(v: f64) -> bool {
    v.is_finite() && v >= 0.0
}

/// 校验创建请求，对应 Go 版 `validateAPIKeyCreateRequest`（handler 层）+ `binding`。
pub fn validate_create_request(req: &CreateApiKeyRequest) -> Result<(), CreateValidationError> {
    // 名称必填。Go 的 binding:"required" 视空串为缺失。
    match req.name.as_deref() {
        Some(n) if !n.is_empty() => {}
        _ => return Err(CreateValidationError::NameRequired),
    }

    for (field, v) in [
        ("quota", req.quota),
        ("rate_limit_5h", req.rate_limit_5h),
        ("rate_limit_1d", req.rate_limit_1d),
        ("rate_limit_7d", req.rate_limit_7d),
    ] {
        if let Some(v) = v {
            if !valid_limit(v) {
                return Err(CreateValidationError::InvalidLimit(field));
            }
        }
    }

    if let Some(days) = req.expires_in_days {
        if days <= 0 {
            return Err(CreateValidationError::InvalidExpiry);
        }
    }

    Ok(())
}

/// 自定义 key 校验失败原因。
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum CustomKeyError {
    TooShort,
    InvalidChars,
}

impl CustomKeyError {
    /// 对应 Go 版的 reason 与文案。
    pub fn code_and_message(&self) -> (&'static str, &'static str) {
        match self {
            CustomKeyError::TooShort => (
                "API_KEY_TOO_SHORT",
                "api key must be at least 16 characters",
            ),
            CustomKeyError::InvalidChars => (
                "API_KEY_INVALID_CHARS",
                "api key can only contain letters, numbers, underscores, and hyphens",
            ),
        }
    }
}

/// 校验自定义 key，对应 Go 版 `ValidateCustomKey`。
///
/// 规则：长度 ≥ 16（按**字节**，与 Go 的 `len(key)` 一致）；
/// 字符仅允许字母、数字、下划线、连字符。
pub fn validate_custom_key(key: &str) -> Result<(), CustomKeyError> {
    if key.len() < CUSTOM_KEY_MIN_LEN {
        return Err(CustomKeyError::TooShort);
    }
    let ok = key
        .bytes()
        .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-');
    if !ok {
        return Err(CustomKeyError::InvalidChars);
    }
    Ok(())
}

/// 生成随机 API Key，对应 Go 版 `GenerateKey`。
///
/// 32 字节随机数据 → 十六进制（64 字符）→ 加前缀（默认 `sk-`）。
pub fn generate_key(prefix: Option<&str>) -> Option<String> {
    let mut buf = [0u8; 32];
    getrandom::getrandom(&mut buf).ok()?;
    let hex: String = buf.iter().map(|b| format!("{b:02x}")).collect();
    let prefix = match prefix {
        Some(p) if !p.is_empty() => p,
        _ => DEFAULT_API_KEY_PREFIX,
    };
    Some(format!("{prefix}{hex}"))
}

/// HTML 转义名称，对应 Go 版 `html.EscapeString`。
///
/// Go 的 `html.EscapeString` 转义这 5 个字符：
/// `&`→`&amp;`、`'`→`&#39;`、`<`→`&lt;`、`>`→`&gt;`、`"`→`&#34;`。
/// 注意 `"` 转成的是**数字实体** `&#34;`，不是 `&quot;`。
pub fn escape_html_name(name: &str) -> String {
    let mut out = String::with_capacity(name.len());
    for c in name.chars() {
        match c {
            '&' => out.push_str("&amp;"),
            '\'' => out.push_str("&#39;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            '"' => out.push_str("&#34;"),
            _ => out.push(c),
        }
    }
    out
}

/// 校验 fallback 分组，对应 Go 版 `validateAPIKeyFallbackGroup`。
///
/// `fallback` 为 `None` 时直接通过；否则要求：
/// primary 存在、与 fallback 不同 ID、同平台、且 fallback 处于 active。
pub fn validate_fallback_group(
    primary_id: Option<i64>,
    primary_platform: Option<&str>,
    fallback_id: Option<i64>,
    fallback_platform: Option<&str>,
    fallback_active: bool,
) -> bool {
    let Some(fb_id) = fallback_id else {
        return true;
    };
    let Some(p_id) = primary_id else {
        return false;
    };
    if p_id == fb_id {
        return false;
    }
    if primary_platform != fallback_platform {
        return false;
    }
    fallback_active
}

/// 判断用户能否绑定分组，对应 Go 版 `User.CanBindGroup`。
///
/// - 非专属分组且用户未开启「限制公开分组」→ 可绑
/// - 否则需落在 `allowed_groups`
pub fn can_bind_group(
    allowed_groups: &[i64],
    restrict_public_groups: bool,
    group_id: i64,
    is_exclusive: bool,
) -> bool {
    if !is_exclusive && !restrict_public_groups {
        return true;
    }
    allowed_groups.contains(&group_id)
}

/// 订阅型分组的 `subscription_type` 取值，与 Go 版 `SubscriptionTypeSubscription` 一致。
pub const SUBSCRIPTION_TYPE_SUBSCRIPTION: &str = "subscription";

/// 判断分组是否为订阅型，对应 Go 版 `Group.IsSubscriptionType`。
pub fn is_subscription_type(subscription_type: &str) -> bool {
    subscription_type == SUBSCRIPTION_TYPE_SUBSCRIPTION
}

/// 生成删除时的 tombstone key，对应 Go 版 `DeleteWithAudit`。
///
/// 格式 `__deleted__{id}__{纳秒}`，用于释放原 key 占用的唯一键。
pub fn tombstone_key(id: i64, now: DateTime<Utc>) -> String {
    let nanos = now.timestamp_nanos_opt().unwrap_or_default();
    format!("__deleted__{id}__{nanos}")
}

/// 分组不存在：`(HTTP 状态码, reason, message)`。
///
/// 对应 Go 版 `service.ErrGroupNotFound`（`infraerrors.NotFound`）。
/// 与「分组存在但无权绑定」是**两条不同错误路径**，不可混用。
pub fn group_not_found_error() -> (u16, &'static str, &'static str) {
    (404, "GROUP_NOT_FOUND", "group not found")
}

/// 分组存在但当前用户无权绑定：对应 Go 版 `service.ErrGroupNotAllowed`。
pub fn group_not_allowed_error() -> (u16, &'static str, &'static str) {
    (
        403,
        "GROUP_NOT_ALLOWED",
        "user is not allowed to bind this group",
    )
}

/// 兜底分组非法：对应 Go 版 `service.ErrFallbackGroupInvalid`。
pub fn fallback_group_invalid_error() -> (u16, &'static str, &'static str) {
    (
        400,
        "FALLBACK_GROUP_INVALID",
        "fallback group must differ from the primary group and use the same platform",
    )
}

/// 删除成功响应体，对应 Go 版 `gin.H{"message": "API key deleted successfully"}`。
#[derive(Debug, Clone, Serialize)]
pub struct DeleteApiKeyResponse {
    pub message: String,
}

impl Default for DeleteApiKeyResponse {
    fn default() -> Self {
        Self {
            message: "API key deleted successfully".to_string(),
        }
    }
}

/// API Key 状态常量，与 Go 版 `service` 包一致。
pub const STATUS_ACTIVE: &str = "active";
pub const STATUS_INACTIVE: &str = "inactive";
/// 配额耗尽状态（可被扩容/重置自动复活）。
pub const STATUS_QUOTA_EXHAUSTED: &str = "quota_exhausted";
/// 已过期状态（可被清除/延长有效期自动复活）。
pub const STATUS_EXPIRED: &str = "expired";

/// 反序列化辅助：区分「字段缺失」与「显式 null」。
///
/// 用于 `fallback_group_id`。`Option<Option<T>>` 直接配 serde 时二者会塌缩为
/// `None`；Go 版用 `optionalInt64{Set, Value}` 区分，语义必须一致。
fn double_option<'de, D, T>(de: D) -> Result<Option<Option<T>>, D::Error>
where
    D: serde::Deserializer<'de>,
    T: serde::Deserialize<'de>,
{
    serde::Deserialize::deserialize(de).map(Some)
}

/// 更新 API Key 请求，字段名对齐 Go 版 `UpdateAPIKeyRequest`。
#[derive(Debug, Clone, Deserialize, Default)]
pub struct UpdateApiKeyRequest {
    /// 名称；`None` 表示不修改。
    #[serde(default)]
    pub name: Option<String>,
    /// 分组 ID；`None` 表示不修改。
    #[serde(default)]
    pub group_id: Option<i64>,
    /// 兜底分组 ID。外层 `None` = 未提供（不修改）；`Some(None)` = 显式 null（清空）；
    /// `Some(Some(v))` = 设为 v。
    #[serde(default, deserialize_with = "double_option")]
    pub fallback_group_id: Option<Option<i64>>,
    /// 状态；仅允许 `active` / `inactive`（对应 Go 的 `oneof`）。
    #[serde(default)]
    pub status: Option<String>,
    /// IP 白名单：`None` 不修改，`Some([])` 清空。
    #[serde(default)]
    pub ip_whitelist: Option<Vec<String>>,
    /// IP 黑名单：`None` 不修改，`Some([])` 清空。
    #[serde(default)]
    pub ip_blacklist: Option<Vec<String>>,
    /// 配额（USD），0 = 不限。
    #[serde(default)]
    pub quota: Option<f64>,
    /// 过期时间（RFC3339 字符串）。空串表示**清除**过期时间。
    #[serde(default)]
    pub expires_at: Option<String>,
    /// 是否重置已用配额。
    #[serde(default)]
    pub reset_quota: Option<bool>,
    #[serde(default)]
    pub rate_limit_5h: Option<f64>,
    #[serde(default)]
    pub rate_limit_1d: Option<f64>,
    #[serde(default)]
    pub rate_limit_7d: Option<f64>,
    /// 是否重置限流用量（含窗口起点）。
    #[serde(default)]
    pub reset_rate_limit_usage: Option<bool>,
}

/// 更新请求校验失败原因。
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum UpdateValidationError {
    /// 数值限额非法（NaN/Inf/负数）。
    Limit(&'static str),
    /// `status` 不是 `active` / `inactive`。
    Status(String),
    /// `expires_at` 不是合法 RFC3339。
    ExpiresAt(String),
}

impl UpdateValidationError {
    pub fn message(&self) -> String {
        match self {
            UpdateValidationError::Limit(field) => format!(
                "Invalid request: numeric limits must be finite and non-negative ({field})"
            ),
            UpdateValidationError::Status(v) => format!(
                "Invalid request: Key: 'UpdateAPIKeyRequest.Status' Error:Field validation for 'Status' failed on the 'oneof' tag (got {v:?})"
            ),
            UpdateValidationError::ExpiresAt(e) => {
                format!("Invalid expires_at format: {e}")
            }
        }
    }
}

/// 校验更新请求，对应 Go 版 `validateAPIKeyUpdateRequest` + handler 的 `binding`。
pub fn validate_update_request(req: &UpdateApiKeyRequest) -> Result<(), UpdateValidationError> {
    for (field, v) in [
        ("quota", req.quota),
        ("rate_limit_5h", req.rate_limit_5h),
        ("rate_limit_1d", req.rate_limit_1d),
        ("rate_limit_7d", req.rate_limit_7d),
    ] {
        if let Some(v) = v {
            if !valid_limit(v) {
                return Err(UpdateValidationError::Limit(field));
            }
        }
    }

    if let Some(s) = req.status.as_deref() {
        // Go 的 binding:"omitempty,oneof=active inactive"：空串视为「未提供」。
        if !s.is_empty() && s != STATUS_ACTIVE && s != STATUS_INACTIVE {
            return Err(UpdateValidationError::Status(s.to_string()));
        }
    }

    // expires_at 非空时必须能解析为 RFC3339；空串表示清除。
    if let Some(raw) = req.expires_at.as_deref() {
        if !raw.is_empty() && chrono::DateTime::parse_from_rfc3339(raw).is_err() {
            return Err(UpdateValidationError::ExpiresAt(raw.to_string()));
        }
    }

    Ok(())
}

/// 待写入的字段与新值。`None` 一律表示「不修改」。
///
/// 与 Go 版 `APIKeyUpdateFields` 掩码等价，但用 `Option` 同时承载「是否修改」与「新值」。
#[derive(Debug, Clone, Default, PartialEq)]
pub struct UpdatePlan {
    pub name: Option<String>,
    pub status: Option<String>,
    pub quota: Option<f64>,
    /// `Some(0.0)` 表示重置已用配额。
    pub quota_used: Option<f64>,
    pub rate_limit_5h: Option<f64>,
    pub rate_limit_1d: Option<f64>,
    pub rate_limit_7d: Option<f64>,
    /// 为真时把 usage_* 归零并把窗口起点置 NULL。
    pub reset_rate_limit_usage: bool,
    /// 外层 `None` = 不修改；`Some(None)` = 清空；`Some(Some(v))` = 设为 v。
    pub group_id: Option<Option<i64>>,
    pub fallback_group_id: Option<Option<i64>>,
    /// 外层 `None` = 不修改；`Some(None)` = 清除；`Some(Some(dt))` = 设为 dt。
    pub expires_at: Option<Option<DateTime<Utc>>>,
    /// `Some([])` 表示清空（写 NULL）。
    pub ip_whitelist: Option<Vec<String>>,
    pub ip_blacklist: Option<Vec<String>>,
}

impl UpdatePlan {
    /// 是否不写任何列（对应 Go 的 `APIKeyUpdateFields.IsEmpty()`）。
    pub fn is_empty(&self) -> bool {
        self.name.is_none()
            && self.status.is_none()
            && self.quota.is_none()
            && self.quota_used.is_none()
            && self.rate_limit_5h.is_none()
            && self.rate_limit_1d.is_none()
            && self.rate_limit_7d.is_none()
            && !self.reset_rate_limit_usage
            && self.group_id.is_none()
            && self.fallback_group_id.is_none()
            && self.expires_at.is_none()
            && self.ip_whitelist.is_none()
            && self.ip_blacklist.is_none()
    }
}

/// 计算更新计划，对应 Go 版 `APIKeyService.Update` 的字段处理与自动复活分支。
///
/// 分组解析（需要查库与权限校验）由调用方完成，通过 `new_group_id` /
/// `new_fallback_group_id` 传入：
/// - `None` 表示请求未涉及该字段
/// - `Some(v)` 表示请求要求改为 v（`v` 为 `None` 即清空）
pub fn compute_update_plan(
    current: &ApiKeyRecord,
    req: &UpdateApiKeyRequest,
    new_group_id: Option<Option<i64>>,
    new_fallback_group_id: Option<Option<i64>>,
    now: DateTime<Utc>,
) -> UpdatePlan {
    let mut plan = UpdatePlan::default();

    // 名称（入库前 HTML 转义）。
    if let Some(name) = req.name.as_deref() {
        plan.name = Some(escape_html_name(name));
    }

    // 分组 / 兜底分组。
    if let Some(g) = new_group_id {
        plan.group_id = Some(g);
    }
    if let Some(f) = new_fallback_group_id {
        plan.fallback_group_id = Some(f);
    }

    // 状态：以「请求前的状态」为基准判定是否需要写库。
    // ⚠️ original_status 必须在应用 req.status **之前**记录，否则显式改状态会被
    // 误判为「无变化」而不写库（该行为由单元测试 explicit_status_override 锁定）。
    let original_status = current.status.clone();
    let mut status = current.status.clone();
    if let Some(s) = req.status.as_deref() {
        if !s.is_empty() {
            status = s.to_string();
        }
    }

    // 配额：扩容或改为不限时，复活 quota_exhausted。
    if let Some(q) = req.quota {
        plan.quota = Some(q);
        if status == STATUS_QUOTA_EXHAUSTED && (q <= 0.0 || q > current.quota_used) {
            status = STATUS_ACTIVE.to_string();
        }
    }

    // 重置已用配额：归零并复活 quota_exhausted。
    if req.reset_quota == Some(true) {
        plan.quota_used = Some(0.0);
        if status == STATUS_QUOTA_EXHAUSTED {
            status = STATUS_ACTIVE.to_string();
        }
    }

    // 过期时间：清除或显式设置；两种情况都可能复活 expired。
    match req.expires_at.as_deref() {
        Some("") => {
            // 清除过期时间。
            plan.expires_at = Some(None);
            if status == STATUS_EXPIRED {
                status = STATUS_ACTIVE.to_string();
            }
        }
        Some(raw) => {
            if let Ok(dt) = chrono::DateTime::parse_from_rfc3339(raw) {
                let dt_utc: DateTime<Utc> = dt.into();
                plan.expires_at = Some(Some(dt_utc));
                // 延长到未来时复活 expired。
                if status == STATUS_EXPIRED && now < dt_utc {
                    status = STATUS_ACTIVE.to_string();
                }
            }
        }
        None => {}
    }

    // IP 限制：nil 不修改；空数组清空。
    if let Some(wl) = req.ip_whitelist.as_ref() {
        plan.ip_whitelist = Some(wl.clone());
    }
    if let Some(bl) = req.ip_blacklist.as_ref() {
        plan.ip_blacklist = Some(bl.clone());
    }

    // 限流阈值。
    if let Some(v) = req.rate_limit_5h {
        plan.rate_limit_5h = Some(v);
    }
    if let Some(v) = req.rate_limit_1d {
        plan.rate_limit_1d = Some(v);
    }
    if let Some(v) = req.rate_limit_7d {
        plan.rate_limit_7d = Some(v);
    }

    // 重置限流用量（含窗口起点）。
    if req.reset_rate_limit_usage == Some(true) {
        plan.reset_rate_limit_usage = true;
    }

    // 自动复活分支改过状态时，统一登记写入。
    if status != original_status || plan.status.is_some() {
        plan.status = Some(status);
    }

    plan
}

#[cfg(test)]
mod tests {
    use super::*;

    fn req(name: Option<&str>) -> CreateApiKeyRequest {
        CreateApiKeyRequest {
            name: name.map(|s| s.to_string()),
            ..Default::default()
        }
    }

    // ── 创建校验 ──

    #[test]
    fn name_is_required() {
        assert_eq!(
            validate_create_request(&req(None)),
            Err(CreateValidationError::NameRequired)
        );
        assert_eq!(
            validate_create_request(&req(Some(""))),
            Err(CreateValidationError::NameRequired)
        );
        assert!(validate_create_request(&req(Some("ok"))).is_ok());
    }

    #[test]
    fn negative_limits_rejected() {
        let mut r = req(Some("k"));
        r.quota = Some(-1.0);
        assert_eq!(
            validate_create_request(&r),
            Err(CreateValidationError::InvalidLimit("quota"))
        );

        let mut r2 = req(Some("k"));
        r2.rate_limit_5h = Some(-0.5);
        assert_eq!(
            validate_create_request(&r2),
            Err(CreateValidationError::InvalidLimit("rate_limit_5h"))
        );
    }

    /// NaN / Inf 必须被拒绝（Go 用 math.IsNaN / IsInf）。
    #[test]
    fn non_finite_limits_rejected() {
        for v in [f64::NAN, f64::INFINITY, f64::NEG_INFINITY] {
            let mut r = req(Some("k"));
            r.quota = Some(v);
            assert!(validate_create_request(&r).is_err(), "{v} 应被拒绝");
        }
    }

    /// 0 是合法值（表示不限额）。
    #[test]
    fn zero_limit_is_valid() {
        let mut r = req(Some("k"));
        r.quota = Some(0.0);
        assert!(validate_create_request(&r).is_ok());
        assert!(valid_limit(0.0));
    }

    #[test]
    fn expiry_must_be_positive() {
        let mut r = req(Some("k"));
        r.expires_in_days = Some(0);
        assert_eq!(
            validate_create_request(&r),
            Err(CreateValidationError::InvalidExpiry)
        );

        r.expires_in_days = Some(-1);
        assert!(validate_create_request(&r).is_err());

        r.expires_in_days = Some(30);
        assert!(validate_create_request(&r).is_ok());
    }

    // ── 自定义 key ──

    #[test]
    fn custom_key_min_length_is_16() {
        assert_eq!(
            validate_custom_key(&"a".repeat(15)),
            Err(CustomKeyError::TooShort)
        );
        assert!(validate_custom_key(&"a".repeat(16)).is_ok());
    }

    #[test]
    fn custom_key_allows_alnum_underscore_hyphen() {
        // 注意：必须 ≥ 16 个字符，否则会先触发 TooShort。
        assert!(validate_custom_key("abcXYZ012_-abcXYZ0").is_ok());
        assert_eq!("abcXYZ012_-abcXYZ0".len(), 18);
    }

    /// 15 个合法字符仍然太短（边界：长度校验先于字符校验）。
    #[test]
    fn valid_chars_but_too_short_still_rejected() {
        assert_eq!(
            validate_custom_key("abcXYZ012_-ab"),
            Err(CustomKeyError::TooShort)
        );
    }

    #[test]
    fn custom_key_rejects_other_chars() {
        assert_eq!(
            validate_custom_key("abcdefghijklmnop!"),
            Err(CustomKeyError::InvalidChars)
        );
        assert_eq!(
            validate_custom_key("abcdefghijklmno@"),
            Err(CustomKeyError::InvalidChars)
        );
        // 空格也不允许
        assert_eq!(
            validate_custom_key("abcdefghijklmno "),
            Err(CustomKeyError::InvalidChars)
        );
    }

    /// 长度按字节计（与 Go 的 len() 一致）：16 个中文 = 48 字节，长度够但字符非法。
    #[test]
    fn custom_key_multibyte_counts_bytes() {
        assert_eq!(
            validate_custom_key("中文中文中文中文中文中文"),
            Err(CustomKeyError::InvalidChars)
        );
    }

    // ── key 生成 ──

    #[test]
    fn generated_key_has_prefix_and_64_hex_chars() {
        let k = generate_key(None).unwrap();
        assert!(k.starts_with("sk-"));
        let hex = &k[3..];
        assert_eq!(hex.len(), 64);
        assert!(hex.chars().all(|c| c.is_ascii_hexdigit()));
    }

    #[test]
    fn generated_key_uses_custom_prefix() {
        let k = generate_key(Some("pk-")).unwrap();
        assert!(k.starts_with("pk-"));
    }

    /// 空前缀回退到默认值（与 Go 的 `if prefix == ""` 一致）。
    #[test]
    fn empty_prefix_falls_back_to_default() {
        let k = generate_key(Some("")).unwrap();
        assert!(k.starts_with("sk-"));
    }

    #[test]
    fn generated_keys_are_unique() {
        assert_ne!(generate_key(None).unwrap(), generate_key(None).unwrap());
    }

    // ── HTML 转义 ──

    #[test]
    fn html_escape_matches_go() {
        assert_eq!(escape_html_name("a&b"), "a&amp;b");
        assert_eq!(escape_html_name("<script>"), "&lt;script&gt;");
        // Go 的 html.EscapeString 对双引号输出 &#34;（数字实体）。
        assert_eq!(escape_html_name(r#"say "hi""#), "say &#34;hi&#34;");
        assert_eq!(escape_html_name("it's"), "it&#39;s");
    }

    #[test]
    fn html_escape_leaves_plain_text_untouched() {
        assert_eq!(escape_html_name("my-key-01"), "my-key-01");
        assert_eq!(escape_html_name(""), "");
    }

    // ── fallback 分组 ──

    #[test]
    fn no_fallback_always_valid() {
        assert!(validate_fallback_group(None, None, None, None, false));
        assert!(validate_fallback_group(
            Some(1),
            Some("openai"),
            None,
            None,
            false
        ));
    }

    #[test]
    fn fallback_without_primary_is_invalid() {
        assert!(!validate_fallback_group(
            None,
            None,
            Some(2),
            Some("openai"),
            true
        ));
    }

    #[test]
    fn fallback_same_id_is_invalid() {
        assert!(!validate_fallback_group(
            Some(1),
            Some("openai"),
            Some(1),
            Some("openai"),
            true
        ));
    }

    #[test]
    fn fallback_different_platform_is_invalid() {
        assert!(!validate_fallback_group(
            Some(1),
            Some("openai"),
            Some(2),
            Some("anthropic"),
            true
        ));
    }

    #[test]
    fn fallback_inactive_is_invalid() {
        assert!(!validate_fallback_group(
            Some(1),
            Some("openai"),
            Some(2),
            Some("openai"),
            false
        ));
    }

    #[test]
    fn fallback_valid_case() {
        assert!(validate_fallback_group(
            Some(1),
            Some("openai"),
            Some(2),
            Some("openai"),
            true
        ));
    }

    // ── 分组绑定权限 ──

    #[test]
    fn public_group_bindable_by_default() {
        assert!(can_bind_group(&[], false, 10, false));
    }

    #[test]
    fn exclusive_group_requires_allowed() {
        assert!(!can_bind_group(&[], false, 10, true));
        assert!(can_bind_group(&[10], false, 10, true));
        assert!(!can_bind_group(&[11], false, 10, true));
    }

    #[test]
    fn restricted_user_needs_allowed_even_for_public() {
        assert!(!can_bind_group(&[], true, 10, false));
        assert!(can_bind_group(&[10], true, 10, false));
    }

    #[test]
    fn subscription_type_detection() {
        assert!(is_subscription_type("subscription"));
        assert!(!is_subscription_type("standard"));
        assert!(!is_subscription_type(""));
    }

    /// 分组不存在与分组不可绑是两条**不同**错误路径。
    ///
    /// - 不存在 → 404 `GROUP_NOT_FOUND`（Go 的 `service.ErrGroupNotFound`）
    /// - 存在但无权 → 403 `GROUP_NOT_ALLOWED`（Go 的 `service.ErrGroupNotAllowed`）
    #[test]
    fn group_error_codes_are_distinct() {
        let nf = group_not_found_error();
        let na = group_not_allowed_error();
        assert_eq!(nf.0, 404);
        assert_eq!(nf.1, "GROUP_NOT_FOUND");
        assert_eq!(nf.2, "group not found");
        assert_eq!(na.0, 403);
        assert_eq!(na.1, "GROUP_NOT_ALLOWED");
        assert_eq!(na.2, "user is not allowed to bind this group");
        assert_ne!(nf.1, na.1);
    }

    #[test]
    fn fallback_group_error_literal() {
        let (status, reason, message) = fallback_group_invalid_error();
        assert_eq!(status, 400);
        assert_eq!(reason, "FALLBACK_GROUP_INVALID");
        assert_eq!(
            message,
            "fallback group must differ from the primary group and use the same platform"
        );
    }

    // ── tombstone ──

    #[test]
    fn tombstone_format_matches_go() {
        let now: DateTime<Utc> = "2026-10-04T02:00:00Z".parse().unwrap();
        let t = tombstone_key(92, now);
        assert!(t.starts_with("__deleted__92__"));
        assert_eq!(
            t,
            format!("__deleted__92__{}", now.timestamp_nanos_opt().unwrap())
        );
    }

    /// 同一 key 的两次删除应产生不同 tombstone（避免唯一键冲突）。
    #[test]
    fn tombstone_differs_by_time() {
        let t1: DateTime<Utc> = "2026-10-04T02:00:00Z".parse().unwrap();
        let t2: DateTime<Utc> = "2026-10-04T02:00:01Z".parse().unwrap();
        assert_ne!(tombstone_key(1, t1), tombstone_key(1, t2));
    }

    #[test]
    fn delete_response_message_matches_go() {
        let v = serde_json::to_value(DeleteApiKeyResponse::default()).unwrap();
        assert_eq!(
            v,
            serde_json::json!({"message": "API key deleted successfully"})
        );
    }

    // ── 更新校验 ──

    #[test]
    fn update_accepts_empty_body() {
        // 空请求体合法（不修改任何字段）。
        let r = UpdateApiKeyRequest::default();
        assert!(validate_update_request(&r).is_ok());
    }

    #[test]
    fn update_rejects_negative_limits() {
        let r = UpdateApiKeyRequest {
            quota: Some(-1.0),
            ..Default::default()
        };
        assert_eq!(
            validate_update_request(&r),
            Err(UpdateValidationError::Limit("quota"))
        );
    }

    #[test]
    fn update_rejects_non_finite_limits() {
        for v in [f64::NAN, f64::INFINITY] {
            let r = UpdateApiKeyRequest {
                rate_limit_1d: Some(v),
                ..Default::default()
            };
            assert!(validate_update_request(&r).is_err());
        }
    }

    #[test]
    fn update_status_whitelist() {
        for ok in ["active", "inactive", ""] {
            let r = UpdateApiKeyRequest {
                status: Some(ok.into()),
                ..Default::default()
            };
            assert!(validate_update_request(&r).is_ok(), "{ok:?} 应通过");
        }

        let bad = UpdateApiKeyRequest {
            status: Some("banned".into()),
            ..Default::default()
        };
        assert!(matches!(
            validate_update_request(&bad),
            Err(UpdateValidationError::Status(_))
        ));
    }

    #[test]
    fn update_expires_at_format() {
        // 空串 = 清除，合法；合法 RFC3339 合法。
        for ok in ["", "2026-12-31T00:00:00Z"] {
            let r = UpdateApiKeyRequest {
                expires_at: Some(ok.into()),
                ..Default::default()
            };
            assert!(validate_update_request(&r).is_ok(), "{ok:?} 应通过");
        }

        let bad = UpdateApiKeyRequest {
            expires_at: Some("not-a-date".into()),
            ..Default::default()
        };
        assert!(matches!(
            validate_update_request(&bad),
            Err(UpdateValidationError::ExpiresAt(_))
        ));
    }

    /// `fallback_group_id` 必须能区分「缺失」与「显式 null」。
    #[test]
    fn update_distinguishes_missing_from_null_fallback() {
        let missing: UpdateApiKeyRequest = serde_json::from_str("{}").unwrap();
        assert!(missing.fallback_group_id.is_none(), "缺失应为 None");

        let null_val: UpdateApiKeyRequest =
            serde_json::from_str(r#"{"fallback_group_id":null}"#).unwrap();
        assert_eq!(
            null_val.fallback_group_id,
            Some(None),
            "显式 null 应为 Some(None)"
        );

        let with_val: UpdateApiKeyRequest =
            serde_json::from_str(r#"{"fallback_group_id":7}"#).unwrap();
        assert_eq!(with_val.fallback_group_id, Some(Some(7)));
    }

    // ── 更新计划 ──

    fn key_with_status(status: &str, quota_used: f64) -> ApiKeyRecord {
        ApiKeyRecord {
            id: 1,
            user_id: 1,
            key: "sk-x".into(),
            name: "n".into(),
            group_id: None,
            fallback_group_id: None,
            status: status.into(),
            ip_whitelist: None,
            ip_blacklist: None,
            last_used_at: None,
            last_used_ip: None,
            quota: 0.0,
            quota_used,
            expires_at: None,
            created_at: "2026-01-01T00:00:00Z".parse().unwrap(),
            updated_at: "2026-01-01T00:00:00Z".parse().unwrap(),
            rate_limit_5h: 0.0,
            rate_limit_1d: 0.0,
            rate_limit_7d: 0.0,
            usage_5h: 0.0,
            usage_1d: 0.0,
            usage_7d: 0.0,
            window_5h_start: None,
            window_1d_start: None,
            window_7d_start: None,
        }
    }

    fn now() -> DateTime<Utc> {
        "2026-06-01T00:00:00Z".parse().unwrap()
    }

    #[test]
    fn plan_empty_for_noop_request() {
        let cur = key_with_status("active", 0.0);
        let plan = compute_update_plan(&cur, &UpdateApiKeyRequest::default(), None, None, now());
        assert!(plan.is_empty(), "无字段变更时空计划");
    }

    /// 名称在计划阶段即完成 HTML 转义。
    #[test]
    fn plan_escapes_name() {
        let cur = key_with_status("active", 0.0);
        let req = UpdateApiKeyRequest {
            name: Some("<i>n</i>".into()),
            ..Default::default()
        };
        let plan = compute_update_plan(&cur, &req, None, None, now());
        assert_eq!(plan.name.as_deref(), Some("&lt;i&gt;n&lt;/i&gt;"));
    }

    /// 扩容配额应复活 quota_exhausted。
    #[test]
    fn quota_increase_reactivates_exhausted() {
        let cur = key_with_status(STATUS_QUOTA_EXHAUSTED, 10.0);
        let req = UpdateApiKeyRequest {
            quota: Some(20.0),
            ..Default::default()
        };
        let plan = compute_update_plan(&cur, &req, None, None, now());
        assert_eq!(plan.status.as_deref(), Some(STATUS_ACTIVE));
        assert_eq!(plan.quota, Some(20.0));
    }

    /// 设为不限（0）同样复活。
    #[test]
    fn quota_to_unlimited_reactivates_exhausted() {
        let cur = key_with_status(STATUS_QUOTA_EXHAUSTED, 10.0);
        let req = UpdateApiKeyRequest {
            quota: Some(0.0),
            ..Default::default()
        };
        let plan = compute_update_plan(&cur, &req, None, None, now());
        assert_eq!(plan.status.as_deref(), Some(STATUS_ACTIVE));
    }

    /// 配额仍不足时不复活。
    #[test]
    fn quota_still_exhausted_stays_exhausted() {
        let cur = key_with_status(STATUS_QUOTA_EXHAUSTED, 10.0);
        let req = UpdateApiKeyRequest {
            quota: Some(5.0), // 仍小于已用
            ..Default::default()
        };
        let plan = compute_update_plan(&cur, &req, None, None, now());
        assert_eq!(plan.status, None, "状态不变则不应写 status 列");
    }

    /// 重置配额：已用量归零并复活。
    #[test]
    fn reset_quota_zeroes_and_reactivates() {
        let cur = key_with_status(STATUS_QUOTA_EXHAUSTED, 10.0);
        let req = UpdateApiKeyRequest {
            reset_quota: Some(true),
            ..Default::default()
        };
        let plan = compute_update_plan(&cur, &req, None, None, now());
        assert_eq!(plan.quota_used, Some(0.0));
        assert_eq!(plan.status.as_deref(), Some(STATUS_ACTIVE));
    }

    /// 清除过期时间应复活 expired。
    #[test]
    fn clear_expiry_reactivates_expired() {
        let cur = key_with_status(STATUS_EXPIRED, 0.0);
        let req = UpdateApiKeyRequest {
            expires_at: Some(String::new()),
            ..Default::default()
        };
        let plan = compute_update_plan(&cur, &req, None, None, now());
        assert_eq!(plan.expires_at, Some(None), "应为清除");
        assert_eq!(plan.status.as_deref(), Some(STATUS_ACTIVE));
    }

    /// 延长有效期到未来应复活 expired。
    #[test]
    fn extend_expiry_reactivates_expired() {
        let cur = key_with_status(STATUS_EXPIRED, 0.0);
        let req = UpdateApiKeyRequest {
            expires_at: Some("2027-01-01T00:00:00Z".into()),
            ..Default::default()
        };
        let plan = compute_update_plan(&cur, &req, None, None, now());
        assert!(matches!(plan.expires_at, Some(Some(_))));
        assert_eq!(plan.status.as_deref(), Some(STATUS_ACTIVE));
    }

    /// 设置为过去时间不复活。
    #[test]
    fn past_expiry_does_not_reactivate() {
        let cur = key_with_status(STATUS_EXPIRED, 0.0);
        let req = UpdateApiKeyRequest {
            expires_at: Some("2026-01-01T00:00:00Z".into()), // 早于 now()
            ..Default::default()
        };
        let plan = compute_update_plan(&cur, &req, None, None, now());
        assert_eq!(plan.status, None, "不复活则不应写 status 列");
    }

    /// IP 规则：空数组表示清空。
    #[test]
    fn empty_ip_list_means_clear() {
        let cur = key_with_status("active", 0.0);
        let req = UpdateApiKeyRequest {
            ip_whitelist: Some(vec![]),
            ..Default::default()
        };
        let plan = compute_update_plan(&cur, &req, None, None, now());
        assert_eq!(plan.ip_whitelist, Some(vec![]));
    }

    /// 显式 null 与缺失在计划中表现不同。
    #[test]
    fn group_clear_vs_unchanged() {
        let cur = key_with_status("active", 0.0);

        // 未提供 → 不修改。
        let plan1 = compute_update_plan(&cur, &UpdateApiKeyRequest::default(), None, None, now());
        assert!(plan1.group_id.is_none());

        // 显式清空。
        let plan2 = compute_update_plan(
            &cur,
            &UpdateApiKeyRequest::default(),
            Some(None),
            None,
            now(),
        );
        assert_eq!(plan2.group_id, Some(None));
    }

    #[test]
    fn reset_rate_limit_usage_flag() {
        let cur = key_with_status("active", 0.0);
        let req = UpdateApiKeyRequest {
            reset_rate_limit_usage: Some(true),
            ..Default::default()
        };
        let plan = compute_update_plan(&cur, &req, None, None, now());
        assert!(plan.reset_rate_limit_usage);
        assert!(!plan.is_empty());
    }

    /// 显式把状态改成 inactive 应按请求生效。
    #[test]
    fn explicit_status_override() {
        let cur = key_with_status("active", 0.0);
        let req = UpdateApiKeyRequest {
            status: Some(STATUS_INACTIVE.into()),
            ..Default::default()
        };
        let plan = compute_update_plan(&cur, &req, None, None, now());
        assert_eq!(plan.status.as_deref(), Some(STATUS_INACTIVE));
    }
}
