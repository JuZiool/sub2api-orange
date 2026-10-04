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
}
