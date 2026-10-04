//! `GET /api/v1/user/profile` 响应构造。
//!
//! 对齐 Go 版 `internal/handler/user_handler.go` 的 `userProfileResponse` 及其
//! 构造链 `userProfileResponseFromService` / `dto.UserFromServiceShallow`。
//!
//! ## 字段顺序与省略行为
//!
//! 响应为 `dto.User` 的字段**内联**展开（Go 用结构体嵌入），后接 profile 专有字段。
//! 为保证与 Go 版一致，这里显式按 Go 的结构体顺序声明字段，并复刻其 `omitempty`。

use chrono::{DateTime, Utc};
use serde::Serialize;

use sub2api_auth::identity::IdentitySummarySet;

use crate::repository::user_repo::User;

/// 通知邮箱条目，对应 Go 版 `dto.NotifyEmailEntry`。
#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct NotifyEmailEntry {
    pub email: String,
    pub disabled: bool,
    pub verified: bool,
}

/// 资料字段的来源追踪，对应 Go 版 `userProfileSourceContext`。
#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct ProfileSourceContext {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub provider: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub source: String,
}

/// `GET /api/v1/user/profile` 的响应体。
///
/// 前 19 个字段内联自 `dto.User`（与 Go 的嵌入展开一致），其后为 profile 专有字段。
#[derive(Debug, Clone, Serialize)]
pub struct UserProfileResponse {
    // ── dto.User（内联展开）──
    pub id: i64,
    pub email: String,
    pub username: String,
    pub role: String,
    pub balance: f64,
    pub frozen_balance: f64,
    pub concurrency: i32,
    pub status: String,
    pub allowed_groups: Vec<i64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub last_active_at: Option<DateTime<Utc>>,
    pub created_at: DateTime<Utc>,
    pub updated_at: DateTime<Utc>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub deleted_at: Option<DateTime<Utc>>,
    pub balance_notify_enabled: bool,
    pub balance_notify_threshold_type: String,
    pub balance_notify_threshold: Option<f64>,
    /// Go 版该字段为 `[]NotifyEmailEntry`，nil 时序列化为 `null`（无 omitempty）。
    pub balance_notify_extra_emails: Vec<NotifyEmailEntry>,
    pub total_recharged: f64,
    pub rpm_limit: i32,

    // ── profile 专有字段 ──
    #[serde(skip_serializing_if = "String::is_empty")]
    pub avatar_url: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub avatar_source: Option<ProfileSourceContext>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub username_source: Option<ProfileSourceContext>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub display_name_source: Option<ProfileSourceContext>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub nickname_source: Option<ProfileSourceContext>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub profile_sources: Option<serde_json::Map<String, serde_json::Value>>,
    pub identities: IdentitySummarySet,
    pub auth_bindings: serde_json::Map<String, serde_json::Value>,
    pub identity_bindings: serde_json::Map<String, serde_json::Value>,
    pub email_bound: bool,
    pub linuxdo_bound: bool,
    pub oidc_bound: bool,
    pub wechat_bound: bool,
    pub dingtalk_bound: bool,
}

/// 解析 `balance_notify_extra_emails`。
///
/// 该列为 **TEXT**，内容是 JSON 数组，元素形如
/// `{"email":"a@x.com","disabled":false,"verified":true}`。
/// 解析失败或格式不符时返回空列表（不中断资料响应）。
pub fn parse_notify_emails(raw: &str) -> Vec<NotifyEmailEntry> {
    let Ok(serde_json::Value::Array(items)) = serde_json::from_str::<serde_json::Value>(raw) else {
        return Vec::new();
    };

    items
        .into_iter()
        .filter_map(|item| {
            let obj = item.as_object()?;
            Some(NotifyEmailEntry {
                email: obj
                    .get("email")
                    .and_then(|v| v.as_str())
                    .unwrap_or_default()
                    .to_string(),
                disabled: obj
                    .get("disabled")
                    .and_then(|v| v.as_bool())
                    .unwrap_or(false),
                verified: obj
                    .get("verified")
                    .and_then(|v| v.as_bool())
                    .unwrap_or(false),
            })
        })
        .collect()
}

/// 构造 profile 响应。
///
/// `user` 为数据库实体，`identities` 为身份摘要集合，`notify_emails_raw` 为
/// `balance_notify_extra_emails` 列的原始文本。
pub fn build_profile_response(
    user: &User,
    identities: IdentitySummarySet,
    notify_emails_raw: &str,
) -> UserProfileResponse {
    let bindings = identities.binding_map();
    let email_bound = identities.email.bound;
    let linuxdo_bound = identities.linuxdo.bound;
    let oidc_bound = identities.oidc.bound;
    let wechat_bound = identities.wechat.bound;
    let dingtalk_bound = identities.dingtalk.bound;

    UserProfileResponse {
        id: user.id,
        email: user.email.clone(),
        username: user.username.clone(),
        role: user.role.clone(),
        balance: user.balance,
        frozen_balance: user.frozen_balance,
        concurrency: user.concurrency,
        status: user.status.clone(),
        allowed_groups: user.allowed_groups.clone(),
        last_active_at: user.last_active_at,
        created_at: user.created_at,
        updated_at: user.updated_at,
        deleted_at: user.deleted_at,
        balance_notify_enabled: user.balance_notify_enabled,
        balance_notify_threshold_type: user.balance_notify_threshold_type.clone(),
        balance_notify_threshold: user.balance_notify_threshold,
        balance_notify_extra_emails: parse_notify_emails(notify_emails_raw),
        total_recharged: user.total_recharged,
        rpm_limit: user.rpm_limit,

        // Go 版这些字段来自身份推断（inferUserProfileSources）；身份模块已提供
        // 摘要，但来源推断依赖第三方身份的 avatar/username 比对，待后续期次补齐。
        avatar_url: String::new(),
        avatar_source: None,
        username_source: None,
        display_name_source: None,
        nickname_source: None,
        profile_sources: None,
        auth_bindings: bindings.clone(),
        identity_bindings: bindings,
        identities,
        email_bound,
        linuxdo_bound,
        oidc_bound,
        wechat_bound,
        dingtalk_bound,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use sub2api_auth::identity::build_identity_summary_set;

    fn sample_user() -> User {
        User {
            id: 11,
            email: "identity@example.com".to_string(),
            username: "identity-user".to_string(),
            role: "user".to_string(),
            balance: 12.5,
            frozen_balance: 0.5,
            concurrency: 5,
            status: "active".to_string(),
            allowed_groups: vec![1, 2],
            last_active_at: None,
            created_at: "2026-01-01T00:00:00Z".parse().unwrap(),
            updated_at: "2026-01-02T00:00:00Z".parse().unwrap(),
            deleted_at: None,
            balance_notify_enabled: true,
            balance_notify_threshold_type: "absolute".to_string(),
            balance_notify_threshold: Some(5.0),
            total_recharged: 100.0,
            rpm_limit: 60,
            signup_source: "email".to_string(),
            password_hash: "h".to_string(),
        }
    }

    #[test]
    fn notify_emails_parsed_from_struct_format() {
        let raw = r#"[{"email":"a@x.com","disabled":false,"verified":true},{"email":"b@x.com","disabled":true,"verified":false}]"#;
        let parsed = parse_notify_emails(raw);
        assert_eq!(parsed.len(), 2);
        assert_eq!(parsed[0].email, "a@x.com");
        assert!(parsed[0].verified);
        assert!(parsed[1].disabled);
    }

    #[test]
    fn notify_emails_legacy_string_array_yields_empty() {
        // 旧格式 []string 无法解析为对象元素，返回空列表而非报错。
        assert!(parse_notify_emails(r#"["a@x.com"]"#).is_empty());
    }

    #[test]
    fn notify_emails_invalid_json_yields_empty() {
        assert!(parse_notify_emails("not json").is_empty());
        assert!(parse_notify_emails("").is_empty());
        assert!(parse_notify_emails("[]").is_empty());
        assert!(parse_notify_emails("{}").is_empty());
    }

    #[test]
    fn profile_response_has_inlined_user_fields() {
        let u = sample_user();
        let identities = build_identity_summary_set(&u.email, &u.signup_source, &[]);
        let resp = build_profile_response(&u, identities, "[]");

        let v = serde_json::to_value(&resp).unwrap();
        // dto.User 内联字段
        assert_eq!(v["id"], 11);
        assert_eq!(v["email"], "identity@example.com");
        assert_eq!(v["balance"], 12.5);
        assert_eq!(v["frozen_balance"], 0.5);
        assert_eq!(v["concurrency"], 5);
        assert_eq!(v["rpm_limit"], 60);
        assert_eq!(v["allowed_groups"], serde_json::json!([1, 2]));
        assert_eq!(v["total_recharged"], 100.0);
    }

    #[test]
    fn profile_response_includes_binding_flags() {
        let u = sample_user();
        let identities = build_identity_summary_set(&u.email, &u.signup_source, &[]);
        let resp = build_profile_response(&u, identities, "[]");
        let v = serde_json::to_value(&resp).unwrap();

        assert_eq!(v["email_bound"], true);
        assert_eq!(v["linuxdo_bound"], false);
        assert_eq!(v["oidc_bound"], false);
        assert_eq!(v["wechat_bound"], false);
        assert_eq!(v["dingtalk_bound"], false);
        assert!(v["identities"]["email"]["bound"].as_bool().unwrap());
    }

    /// `auth_bindings` 与 `identity_bindings` 内容一致（Go 版也是同一份 map）。
    #[test]
    fn auth_and_identity_bindings_are_equal() {
        let u = sample_user();
        let identities = build_identity_summary_set(&u.email, &u.signup_source, &[]);
        let resp = build_profile_response(&u, identities, "[]");
        let v = serde_json::to_value(&resp).unwrap();
        assert_eq!(v["auth_bindings"], v["identity_bindings"]);
    }

    /// `last_active_at` / `deleted_at` 为 None 时省略（对应 Go 的 omitempty）。
    #[test]
    fn optional_timestamps_are_omitted_when_none() {
        let u = sample_user();
        let identities = build_identity_summary_set(&u.email, &u.signup_source, &[]);
        let resp = build_profile_response(&u, identities, "[]");
        let v = serde_json::to_value(&resp).unwrap();
        assert!(v.get("last_active_at").is_none());
        assert!(v.get("deleted_at").is_none());
    }

    /// `balance_notify_extra_emails` 无 omitempty，空列表应序列化为 `[]`。
    #[test]
    fn notify_emails_serialized_even_when_empty() {
        let u = sample_user();
        let identities = build_identity_summary_set(&u.email, &u.signup_source, &[]);
        let resp = build_profile_response(&u, identities, "[]");
        let v = serde_json::to_value(&resp).unwrap();
        assert_eq!(v["balance_notify_extra_emails"], serde_json::json!([]));
    }
}
