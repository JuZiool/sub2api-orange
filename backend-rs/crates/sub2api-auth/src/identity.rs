//! 用户身份摘要构建。
//!
//! 对齐 Go 版 `internal/service/user_service.go` 中的
//! `buildEmailIdentitySummary` / `buildProviderIdentitySummary` / `canUnbindProvider`
//! / `canUseEmailAsSignInMethod` 及其辅助函数。
//!
//! 这些摘要会出现在 `GET /api/v1/user/profile` 的 `identities` / `auth_bindings`
//! / `identity_bindings` 字段中，前端据此渲染「绑定状态」与「可否解绑」。
//!
//! ## 关键规则
//!
//! - **合成邮箱视为「未绑定邮箱」**：`@linuxdo-connect.invalid` 之类的域名是第三方
//!   登录自动生成的占位邮箱，不能当作真实邮箱身份（见 [`is_reserved_email`]）。
//! - **解绑前置条件**：只有当用户**还有其它可登录方式**时才允许解绑（见
//!   [`can_unbind_provider`]），避免把自己锁在账号外。
//! - **排序取主身份**：按 `verified_at` → `updated_at` → `created_at` 倒序，
//!   同刻按 `provider_key` 升序（见 [`select_primary_identity`]）。

use chrono::{DateTime, Utc};
use serde::Serialize;

/// 第三方登录合成邮箱的域名后缀，与 Go 版常量一致。
const LINUXDO_SYNTHETIC_EMAIL_DOMAIN: &str = "@linuxdo-connect.invalid";
const OIDC_SYNTHETIC_EMAIL_DOMAIN: &str = "@oidc-connect.invalid";
const WECHAT_SYNTHETIC_EMAIL_DOMAIN: &str = "@wechat-connect.invalid";
const DINGTALK_SYNTHETIC_EMAIL_DOMAIN: &str = "@dingtalk-connect.invalid";

/// 摘要提示文案的 i18n key，与 Go 版常量逐字一致。
pub const NOTE_KEY_EMAIL_MANAGED_FROM_PROFILE: &str =
    "profile.authBindings.notes.emailManagedFromProfile";
pub const NOTE_KEY_CAN_UNBIND: &str = "profile.authBindings.notes.canUnbind";
pub const NOTE_KEY_BIND_ANOTHER_BEFORE_UNBIND: &str =
    "profile.authBindings.notes.bindAnotherBeforeUnbind";

/// 支持的身份提供方，顺序与 Go 版 `canUnbindProvider` 中的候选列表一致。
pub const THIRD_PARTY_PROVIDERS: [&str; 4] = ["linuxdo", "oidc", "wechat", "dingtalk"];

/// 判断是否为保留（合成）邮箱。与 Go 版 `isReservedEmail` 一致。
pub fn is_reserved_email(email: &str) -> bool {
    let normalized = email.trim().to_lowercase();
    normalized.ends_with(LINUXDO_SYNTHETIC_EMAIL_DOMAIN)
        || normalized.ends_with(OIDC_SYNTHETIC_EMAIL_DOMAIN)
        || normalized.ends_with(WECHAT_SYNTHETIC_EMAIL_DOMAIN)
        || normalized.ends_with(DINGTALK_SYNTHETIC_EMAIL_DOMAIN)
}

/// 邮箱掩码：`alice@x.com` → `a***e@x.com`；单字符本地名 → `a***@x.com`。
///
/// 无 `@` 或任一侧为空时退化为 [`mask_opaque_identity`]。与 Go 版 `maskEmailIdentity` 一致。
pub fn mask_email_identity(email: &str) -> String {
    let trimmed = email.trim();
    let Some((local, domain)) = trimmed.split_once('@') else {
        return mask_opaque_identity(email);
    };
    if local.is_empty() || domain.is_empty() {
        return mask_opaque_identity(email);
    }

    let runes: Vec<char> = local.chars().collect();
    if runes.len() == 1 {
        return format!("{}***@{}", runes[0], domain);
    }
    format!("{}***{}@{}", runes[0], runes[runes.len() - 1], domain)
}

/// 不透明标识掩码，按码点长度分档。与 Go 版 `maskOpaqueIdentity` 一致。
///
/// 注意 Go 版按 **rune**（码点）计数，不是字节，因此这里用 `chars()`。
pub fn mask_opaque_identity(value: &str) -> String {
    let runes: Vec<char> = value.trim().chars().collect();
    match runes.len() {
        0 => String::new(),
        1..=4 => format!("{}***", runes[0]),
        5..=8 => format!(
            "{}***{}",
            runes[..2].iter().collect::<String>(),
            runes[runes.len() - 1]
        ),
        _ => format!(
            "{}***{}",
            runes[..3].iter().collect::<String>(),
            runes[runes.len() - 3..].iter().collect::<String>()
        ),
    }
}

/// 身份记录，对应 Go 版 `UserAuthIdentityRecord` 的所需子集。
#[derive(Debug, Clone, Default)]
pub struct IdentityRecord {
    pub provider_type: String,
    pub provider_key: String,
    pub provider_subject: String,
    pub verified_at: Option<DateTime<Utc>>,
    pub updated_at: Option<DateTime<Utc>>,
    pub created_at: Option<DateTime<Utc>>,
    /// 元数据（JSON 对象），用于取 display_name / avatar_url / source 等。
    pub metadata: serde_json::Map<String, serde_json::Value>,
}

/// 身份摘要，字段与序列化行为对齐 Go 版 `UserIdentitySummary`。
#[derive(Debug, Clone, Serialize, PartialEq, Eq, Default)]
pub struct IdentitySummary {
    pub provider: String,
    pub bound: bool,
    pub bound_count: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub display_name: String,
    // Go 版该字段为 `json:"-"`，即不序列化，这里保持一致（不参与输出）。
    #[serde(skip)]
    pub avatar_url: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub subject_hint: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub provider_key: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub verified_at: Option<DateTime<Utc>>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub bind_start_path: String,
    pub can_bind: bool,
    pub can_unbind: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub note_key: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub note: String,
}

/// 五个提供方的摘要集合，字段名与 Go 版 `UserIdentitySummarySet` 一致。
#[derive(Debug, Clone, Serialize, PartialEq, Eq, Default)]
pub struct IdentitySummarySet {
    pub email: IdentitySummary,
    pub linuxdo: IdentitySummary,
    pub oidc: IdentitySummary,
    pub wechat: IdentitySummary,
    pub dingtalk: IdentitySummary,
}

impl IdentitySummarySet {
    /// 按提供方取摘要（可变），`email` 返回邮箱摘要。
    pub fn get_mut(&mut self, provider: &str) -> Option<&mut IdentitySummary> {
        match provider {
            "email" => Some(&mut self.email),
            "linuxdo" => Some(&mut self.linuxdo),
            "oidc" => Some(&mut self.oidc),
            "wechat" => Some(&mut self.wechat),
            "dingtalk" => Some(&mut self.dingtalk),
            _ => None,
        }
    }

    /// 按提供方取摘要（只读）。
    pub fn get(&self, provider: &str) -> Option<&IdentitySummary> {
        match provider {
            "email" => Some(&self.email),
            "linuxdo" => Some(&self.linuxdo),
            "oidc" => Some(&self.oidc),
            "wechat" => Some(&self.wechat),
            "dingtalk" => Some(&self.dingtalk),
            _ => None,
        }
    }

    /// 构造 `auth_bindings` / `identity_bindings` 映射（键为提供方名）。
    ///
    /// 对应 Go 版 `userProfileBindingMap`，键顺序为 email/linuxdo/oidc/wechat/dingtalk。
    pub fn binding_map(&self) -> serde_json::Map<String, serde_json::Value> {
        let mut map = serde_json::Map::new();
        for (key, summary) in [
            ("email", &self.email),
            ("linuxdo", &self.linuxdo),
            ("oidc", &self.oidc),
            ("wechat", &self.wechat),
            ("dingtalk", &self.dingtalk),
        ] {
            map.insert(
                key.to_string(),
                serde_json::to_value(summary).unwrap_or(serde_json::Value::Null),
            );
        }
        map
    }
}

/// 按提供方过滤记录（大小写不敏感，与 Go 版 `filterUserAuthIdentities` 一致）。
pub fn filter_by_provider<'a>(
    records: &'a [IdentityRecord],
    provider: &str,
) -> Vec<&'a IdentityRecord> {
    records
        .iter()
        .filter(|r| r.provider_type.trim().eq_ignore_ascii_case(provider))
        .collect()
}

/// 取记录的排序时间：`verified_at` → `updated_at` → `created_at`。
fn sort_time(record: &IdentityRecord) -> Option<DateTime<Utc>> {
    record
        .verified_at
        .or(record.updated_at)
        .or(record.created_at)
}

/// 选出主身份：时间倒序，同刻按 `provider_key` 升序。
///
/// 注意 Go 版用 `sort.SliceStable`——稳定排序，因此**同刻同 key 时保持原顺序**。
pub fn select_primary_identity(records: &[&IdentityRecord]) -> Option<IdentityRecord> {
    if records.is_empty() {
        return None;
    }
    let mut sorted: Vec<&IdentityRecord> = records.to_vec();
    sorted.sort_by(|a, b| match (sort_time(a), sort_time(b)) {
        (Some(ta), Some(tb)) => tb
            .cmp(&ta)
            .then_with(|| a.provider_key.cmp(&b.provider_key)),
        (Some(_), None) => std::cmp::Ordering::Less,
        (None, Some(_)) => std::cmp::Ordering::Greater,
        (None, None) => a.provider_key.cmp(&b.provider_key),
    });
    sorted.first().map(|r| (*r).clone())
}

/// 从元数据中按序取首个非空字符串值。与 Go 版 `firstStringIdentityValue` 一致。
pub fn first_string_value(
    metadata: &serde_json::Map<String, serde_json::Value>,
    keys: &[&str],
) -> String {
    for key in keys {
        if let Some(serde_json::Value::String(s)) = metadata.get(*key) {
            let trimmed = s.trim();
            if !trimmed.is_empty() {
                return trimmed.to_string();
            }
        }
    }
    String::new()
}

/// 身份展示名：元数据优先，其次 subject，最后 provider_type。
pub fn identity_display_name(record: &IdentityRecord) -> String {
    let from_meta = first_string_value(
        &record.metadata,
        &[
            "display_name",
            "suggested_display_name",
            "username",
            "name",
            "nickname",
            "email",
        ],
    );
    if !from_meta.is_empty() {
        return from_meta;
    }
    let subject = record.provider_subject.trim();
    if !subject.is_empty() {
        return subject.to_string();
    }
    record.provider_type.trim().to_string()
}

/// 邮箱身份摘要，对齐 Go 版 `buildEmailIdentitySummary`。
pub fn build_email_identity_summary(
    user_email: &str,
    records: &[IdentityRecord],
) -> IdentitySummary {
    let mut summary = IdentitySummary {
        provider: "email".to_string(),
        can_bind: false,
        can_unbind: false,
        note_key: NOTE_KEY_EMAIL_MANAGED_FROM_PROFILE.to_string(),
        note: "Primary account email is managed from the profile form.".to_string(),
        ..Default::default()
    };

    let filtered = filter_by_provider(records, "email");
    if !filtered.is_empty() {
        let Some(primary) = select_primary_identity(&filtered) else {
            return summary;
        };

        let mut email = first_string_value(&primary.metadata, &["email"]);
        if email.is_empty() {
            email = primary.provider_subject.trim().to_string();
        }
        if email.is_empty() || is_reserved_email(&email) {
            email = user_email.trim().to_string();
        }
        if email.is_empty() || is_reserved_email(&email) {
            email = primary.provider_key.trim().to_string();
        }

        summary.bound = true;
        summary.bound_count = filtered.len() as i64;
        summary.display_name = email.clone();
        summary.subject_hint = mask_email_identity(&email);
        summary.provider_key = primary.provider_key.trim().to_string();
        summary.verified_at = primary.verified_at;
        return summary;
    }

    // 兼容回退：早于 auth_identities 回填的旧普通邮箱用户。
    let email = user_email.trim().to_string();
    if email.is_empty() || is_reserved_email(&email) {
        return summary;
    }
    summary.bound = true;
    summary.bound_count = 1;
    summary.display_name = email.clone();
    summary.subject_hint = mask_email_identity(&email);
    summary.provider_key = "email".to_string();
    summary
}

/// 判断邮箱是否可作为登录方式。与 Go 版 `canUseEmailAsSignInMethod` 一致。
pub fn can_use_email_as_sign_in_method(
    user_email: &str,
    signup_source: &str,
    records: &[IdentityRecord],
) -> bool {
    let email = user_email.trim().to_lowercase();
    if email.is_empty() || is_reserved_email(&email) {
        return false;
    }

    if email_signup_source_allows_login(signup_source) {
        return true;
    }

    filter_by_provider(records, "email")
        .iter()
        .any(|r| email_identity_supports_sign_in(r))
}

/// 与 Go 版 `emailSignupSourceAllowsLogin` 一致。
fn email_signup_source_allows_login(signup_source: &str) -> bool {
    let s = signup_source.trim().to_lowercase();
    s.is_empty() || s == "email"
}

/// 与 Go 版 `emailIdentitySupportsSignIn` 一致。
fn email_identity_supports_sign_in(record: &IdentityRecord) -> bool {
    let source = first_string_value(&record.metadata, &["source"]);
    matches!(
        source.as_str(),
        "auth_service_email_bind" | "auth_service_login_backfill" | "auth_service_dual_write"
    )
}

/// 判断某第三方提供方是否可解绑。与 Go 版 `canUnbindProvider` 一致。
pub fn can_unbind_provider(
    provider: &str,
    user_email: &str,
    signup_source: &str,
    records: &[IdentityRecord],
) -> bool {
    if provider.is_empty() || provider == "email" {
        return false;
    }
    if filter_by_provider(records, provider).is_empty() {
        return false;
    }

    if can_use_email_as_sign_in_method(user_email, signup_source, records) {
        return true;
    }

    // 存在其它第三方登录方式即可解绑。
    THIRD_PARTY_PROVIDERS
        .iter()
        .filter(|candidate| **candidate != provider)
        .any(|candidate| !filter_by_provider(records, candidate).is_empty())
}

/// 构建第三方提供方摘要，对齐 Go 版 `buildProviderIdentitySummary`。
pub fn build_provider_identity_summary(
    provider: &str,
    user_email: &str,
    signup_source: &str,
    records: &[IdentityRecord],
) -> IdentitySummary {
    let mut summary = IdentitySummary {
        provider: provider.to_string(),
        can_unbind: false,
        ..Default::default()
    };

    let filtered = filter_by_provider(records, provider);
    if filtered.is_empty() {
        summary.can_bind = true;
        // Go 版会尝试构造绑定入口 URL；若构造失败则留空。
        if let Some(path) = identity_bind_start_path(provider) {
            summary.bind_start_path = path;
        }
        return summary;
    }

    let Some(primary) = select_primary_identity(&filtered) else {
        return summary;
    };

    summary.bound = true;
    summary.bound_count = filtered.len() as i64;
    summary.display_name = identity_display_name(&primary);
    summary.avatar_url = first_string_value(
        &primary.metadata,
        &["avatar_url", "suggested_avatar_url", "headimgurl"],
    );
    summary.subject_hint = mask_opaque_identity(&primary.provider_subject);
    summary.provider_key = primary.provider_key.trim().to_string();
    summary.verified_at = primary.verified_at;

    summary.can_unbind = can_unbind_provider(provider, user_email, signup_source, records);
    if summary.can_unbind {
        summary.note_key = NOTE_KEY_CAN_UNBIND.to_string();
        summary.note = "You can unbind this sign-in method.".to_string();
    } else {
        summary.note_key = NOTE_KEY_BIND_ANOTHER_BEFORE_UNBIND.to_string();
        summary.note = "Bind another sign-in method before unbinding.".to_string();
    }
    summary
}

/// 绑定入口路径，与 Go 版 `buildUserIdentityBindAuthorizeURL` 的路径部分一致。
///
/// 查询串（`redirect` / `intent`）需在真实绑定流程中拼接，此处仅返回路径。
pub fn identity_bind_start_path(provider: &str) -> Option<String> {
    let path = match provider {
        "linuxdo" => "/api/v1/auth/oauth/linuxdo/bind/start",
        "oidc" => "/api/v1/auth/oauth/oidc/bind/start",
        "wechat" => "/api/v1/auth/oauth/wechat/bind/start",
        "dingtalk" => "/api/v1/auth/oauth/dingtalk/bind/start",
        _ => return None,
    };
    Some(path.to_string())
}

/// 构建全部身份摘要，对齐 Go 版 `GetProfileIdentitySummaries`。
///
/// 注：Go 版还会依系统设置关闭未启用提供方的绑定入口
/// （`applyExplicitProviderAvailability`），该部分依赖设置模块，待其实现后接入。
pub fn build_identity_summary_set(
    user_email: &str,
    signup_source: &str,
    records: &[IdentityRecord],
) -> IdentitySummarySet {
    IdentitySummarySet {
        email: build_email_identity_summary(user_email, records),
        linuxdo: build_provider_identity_summary("linuxdo", user_email, signup_source, records),
        oidc: build_provider_identity_summary("oidc", user_email, signup_source, records),
        wechat: build_provider_identity_summary("wechat", user_email, signup_source, records),
        dingtalk: build_provider_identity_summary("dingtalk", user_email, signup_source, records),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn rec(provider: &str, key: &str, subject: &str) -> IdentityRecord {
        IdentityRecord {
            provider_type: provider.to_string(),
            provider_key: key.to_string(),
            provider_subject: subject.to_string(),
            ..Default::default()
        }
    }

    fn meta(pairs: &[(&str, &str)]) -> serde_json::Map<String, serde_json::Value> {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), json!(v)))
            .collect()
    }

    #[test]
    fn reserved_email_domains_detected() {
        for e in [
            "x@linuxdo-connect.invalid",
            "X@OIDC-CONNECT.INVALID",
            "a@wechat-connect.invalid",
            "a@dingtalk-connect.invalid",
        ] {
            assert!(is_reserved_email(e), "{e} 应为保留邮箱");
        }
        assert!(!is_reserved_email("real@example.com"));
    }

    #[test]
    fn email_mask_matches_go() {
        assert_eq!(mask_email_identity("alice@x.com"), "a***e@x.com");
        assert_eq!(mask_email_identity("a@x.com"), "a***@x.com");
        // 无 @ → 退化为不透明掩码。
        assert_eq!(mask_email_identity("abcdefghij"), "abc***hij");
    }

    #[test]
    fn opaque_mask_by_rune_length() {
        assert_eq!(mask_opaque_identity(""), "");
        assert_eq!(mask_opaque_identity("abcd"), "a***");
        assert_eq!(mask_opaque_identity("abcde"), "ab***e");
        assert_eq!(mask_opaque_identity("abcdefgh"), "ab***h");
        assert_eq!(mask_opaque_identity("abcdefghi"), "abc***ghi");
    }

    /// 按码点计数，而非字节：4 个中文（12 字节）应走 `<= 4` 分支。
    #[test]
    fn opaque_mask_counts_runes_not_bytes() {
        assert_eq!(mask_opaque_identity("中文测试"), "中***");
        assert_eq!(mask_opaque_identity("中文测试字"), "中文***字");
    }

    #[test]
    fn email_summary_uses_identity_when_present() {
        let mut r = rec("email", "email", "alice@example.com");
        r.metadata = meta(&[("email", "alice@example.com")]);
        let s = build_email_identity_summary("whatever@x.com", &[r]);
        assert!(s.bound);
        assert_eq!(s.bound_count, 1);
        assert_eq!(s.display_name, "alice@example.com");
        assert_eq!(s.subject_hint, "a***e@example.com");
        assert!(!s.can_bind);
        assert!(!s.can_unbind);
        assert_eq!(s.note_key, NOTE_KEY_EMAIL_MANAGED_FROM_PROFILE);
    }

    /// 身份记录里是合成邮箱时，回退使用用户真实邮箱。
    #[test]
    fn email_summary_falls_back_to_user_email_for_reserved() {
        let mut r = rec("email", "email", "x@linuxdo-connect.invalid");
        r.metadata = meta(&[("email", "x@linuxdo-connect.invalid")]);
        let s = build_email_identity_summary("real@example.com", &[r]);
        assert!(s.bound);
        assert_eq!(s.display_name, "real@example.com");
    }

    /// 无 email 身份记录时的兼容回退（旧用户）。
    #[test]
    fn email_summary_legacy_fallback() {
        let s = build_email_identity_summary("legacy@example.com", &[]);
        assert!(s.bound);
        assert_eq!(s.bound_count, 1);
        assert_eq!(s.display_name, "legacy@example.com");
        assert_eq!(s.provider_key, "email");
    }

    /// 保留邮箱 + 无记录 → 未绑定。
    #[test]
    fn email_summary_reserved_without_record_is_unbound() {
        let s = build_email_identity_summary("x@oidc-connect.invalid", &[]);
        assert!(!s.bound);
        assert_eq!(s.bound_count, 0);
    }

    #[test]
    fn third_party_unbound_can_bind_with_path() {
        let s = build_provider_identity_summary("linuxdo", "a@b.com", "", &[]);
        assert!(!s.bound);
        assert!(s.can_bind);
        assert_eq!(s.bind_start_path, "/api/v1/auth/oauth/linuxdo/bind/start");
    }

    #[test]
    fn bound_provider_display_name_from_metadata() {
        let mut r = rec("linuxdo", "linuxdo", "linuxdo-subject-123456");
        r.metadata = meta(&[("username", "linuxdo-handle")]);
        let s = build_provider_identity_summary("linuxdo", "a@b.com", "", &[r]);
        assert!(s.bound);
        assert_eq!(s.display_name, "linuxdo-handle");
        // subject 掩码：14 字符 → 前 3 后 3。
        assert_eq!(s.subject_hint, "lin***456");
        assert!(!s.can_bind);
    }

    /// 有另一个第三方登录方式 → 可解绑。
    #[test]
    fn can_unbind_when_another_provider_exists() {
        let records = vec![
            rec("linuxdo", "linuxdo", "sub1"),
            rec("oidc", "https://issuer", "sub2"),
        ];
        let s = build_provider_identity_summary("linuxdo", "a@b.com", "", &records);
        assert!(s.can_unbind);
        assert_eq!(s.note_key, NOTE_KEY_CAN_UNBIND);
    }

    /// 仅一种第三方且邮箱不可登录 → 不可解绑（避免自锁）。
    #[test]
    fn cannot_unbind_when_last_login_method() {
        let records = vec![rec("linuxdo", "linuxdo", "sub1")];
        let s = build_provider_identity_summary(
            "linuxdo",
            "x@linuxdo-connect.invalid",
            "oauth",
            &records,
        );
        assert!(!s.can_unbind, "唯一登录方式不应允许解绑");
        assert_eq!(s.note_key, NOTE_KEY_BIND_ANOTHER_BEFORE_UNBIND);
    }

    /// 邮箱可登录时，第三方即可解绑。
    #[test]
    fn can_unbind_when_email_login_available() {
        let records = vec![rec("linuxdo", "linuxdo", "sub1")];
        let s = build_provider_identity_summary("linuxdo", "real@example.com", "email", &records);
        assert!(s.can_unbind);
    }

    /// 邮箱来源非 email 且无可用邮箱身份 → 邮箱不可登录。
    #[test]
    fn email_not_login_method_when_signup_source_oauth() {
        assert!(!can_use_email_as_sign_in_method("a@b.com", "oauth", &[]));
        assert!(can_use_email_as_sign_in_method("a@b.com", "email", &[]));
        assert!(can_use_email_as_sign_in_method("a@b.com", "", &[]));
    }

    /// 邮箱身份带 auth_service_email_bind source → 可登录。
    #[test]
    fn email_identity_source_grants_login() {
        let mut r = rec("email", "email", "a@b.com");
        r.metadata = meta(&[("source", "auth_service_email_bind")]);
        assert!(can_use_email_as_sign_in_method("a@b.com", "oauth", &[r]));
    }

    #[test]
    fn primary_selection_prefers_latest_verified() {
        let older = IdentityRecord {
            verified_at: Some("2020-01-01T00:00:00Z".parse().unwrap()),
            ..rec("oidc", "aaa", "sub-old")
        };
        let newer = IdentityRecord {
            verified_at: Some("2026-01-01T00:00:00Z".parse().unwrap()),
            ..rec("oidc", "bbb", "sub-new")
        };
        let refs: Vec<&IdentityRecord> = vec![&older, &newer];
        let picked = select_primary_identity(&refs).unwrap();
        assert_eq!(picked.provider_key, "bbb");
    }

    #[test]
    fn primary_selection_tiebreaks_by_provider_key() {
        let a = rec("oidc", "zzz", "s1");
        let b = rec("oidc", "aaa", "s2");
        let refs: Vec<&IdentityRecord> = vec![&a, &b];
        let picked = select_primary_identity(&refs).unwrap();
        assert_eq!(picked.provider_key, "aaa", "同刻应按 provider_key 升序");
    }

    #[test]
    fn full_summary_set_builds_all_providers() {
        let set = build_identity_summary_set("real@example.com", "email", &[]);
        assert!(set.email.bound);
        assert!(set.linuxdo.can_bind);
        assert!(set.oidc.can_bind);
        assert!(set.wechat.can_bind);
        assert!(set.dingtalk.can_bind);
    }

    /// 序列化：`avatar_url` 不输出（对应 Go 的 `json:"-"`）。
    #[test]
    fn avatar_url_is_not_serialized() {
        let mut r = rec("linuxdo", "linuxdo", "sub1");
        r.metadata = meta(&[("avatar_url", "https://cdn/a.png")]);
        let s = build_provider_identity_summary("linuxdo", "a@b.com", "", &[r]);
        assert_eq!(s.avatar_url, "https://cdn/a.png");
        let v = serde_json::to_value(&s).unwrap();
        assert!(
            v.get("avatar_url").is_none(),
            "avatar_url 不应出现在 JSON 中"
        );
    }

    /// 绑定映射键名与 Go 版一致。
    #[test]
    fn binding_map_has_expected_keys() {
        let set = build_identity_summary_set("a@b.com", "email", &[]);
        let map = set.binding_map();
        for key in ["email", "linuxdo", "oidc", "wechat", "dingtalk"] {
            assert!(map.contains_key(key), "缺少键 {key}");
        }
    }
}
