//! API Key 的 DTO 与映射。
//!
//! 对齐 Go 版 `internal/handler/dto/types.go` 的 `APIKey` / `Group` 结构，
//! 以及 `mappers.go` 的 `APIKeyFromService` / `groupFromServiceBase`。
//!
//! ## 限流窗口语义（易错点）
//!
//! `usage_5h/1d/7d` 是**窗口内**用量。窗口过期后，对外展示的用量应视为 0，
//! 但**数据库里的原值不动**（由定期任务或下次写入时才重置）。
//! Go 版用 `EffectiveUsage5h/1d/7d` 实现这一点：
//!
//! ```text
//! EffectiveUsage = if 窗口已过期 { 0 } else { 原始 usage }
//! 窗口已过期     = window_start 为空 || (now - window_start) >= 窗口时长
//! ```
//!
//! `reset_5h_at` 等字段则仅在窗口**未**过期时给出（`window_start + 时长`），
//! 过期时为 `null`（对应 Go 的 `omitempty` 指针）。

use std::collections::BTreeMap;
use std::time::Duration;

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};

/// 5 小时窗口，与 Go 版 `RateLimitWindow5h` 一致。
pub const RATE_LIMIT_WINDOW_5H: Duration = Duration::from_secs(5 * 3600);
/// 1 天窗口，与 Go 版 `RateLimitWindow1d` 一致。
pub const RATE_LIMIT_WINDOW_1D: Duration = Duration::from_secs(24 * 3600);
/// 7 天窗口，与 Go 版 `RateLimitWindow7d` 一致。
pub const RATE_LIMIT_WINDOW_7D: Duration = Duration::from_secs(7 * 24 * 3600);

/// 判断限流窗口是否已过期，对应 Go 版 `IsWindowExpired`。
///
/// `window_start` 为 `None` 视为过期（Go 中对 nil 指针同样返回 true）。
pub fn is_window_expired(
    window_start: Option<DateTime<Utc>>,
    duration: Duration,
    now: DateTime<Utc>,
) -> bool {
    let Some(start) = window_start else {
        return true;
    };
    let elapsed = now.signed_duration_since(start);
    elapsed.num_seconds() >= duration.as_secs() as i64
}

/// 窗口内的有效用量：窗口过期时按 0 计。
///
/// 对应 Go 版 `EffectiveUsage5h/1d/7d`。
pub fn effective_usage(
    usage: f64,
    window_start: Option<DateTime<Utc>>,
    duration: Duration,
    now: DateTime<Utc>,
) -> f64 {
    if is_window_expired(window_start, duration, now) {
        0.0
    } else {
        usage
    }
}

/// 窗口重置时间：仅在窗口未过期时给出，对应 Go 版 mapper 中的 `Reset5hAt` 等。
pub fn reset_at(
    window_start: Option<DateTime<Utc>>,
    duration: Duration,
    now: DateTime<Utc>,
) -> Option<DateTime<Utc>> {
    let start = window_start?;
    if is_window_expired(Some(start), duration, now) {
        return None;
    }
    chrono::Duration::from_std(duration).ok().map(|d| start + d)
}

/// 分组级精确匹配倍率规则，对应 Go 版 `domain.ModelRateMultiplierRule`。
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ModelRateMultiplierRule {
    pub model: String,
    pub multiplier: f64,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub hidden: bool,
}

/// 推理强度映射规则，对应 Go 版 `domain.ReasoningEffortMapping`。
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct ReasoningEffortMapping {
    pub from: String,
    pub to: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub match_type: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub model: String,
}

/// 分组 DTO，字段与 Go 版 `dto.Group` 逐一对齐（含省略行为）。
#[derive(Debug, Clone, Serialize)]
pub struct GroupDto {
    pub id: i64,
    pub name: String,
    pub description: String,
    pub platform: String,
    pub rate_multiplier: f64,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub model_rate_multipliers: Vec<ModelRateMultiplierRule>,
    pub is_exclusive: bool,
    pub status: String,

    pub subscription_type: String,
    pub daily_limit_usd: Option<f64>,
    pub weekly_limit_usd: Option<f64>,
    pub monthly_limit_usd: Option<f64>,
    pub long_context_pricing_enabled: bool,

    pub allow_image_generation: bool,
    pub allow_batch_image_generation: bool,
    pub image_rate_independent: bool,
    pub image_rate_multiplier: f64,
    pub batch_image_discount_multiplier: f64,
    pub batch_image_hold_multiplier: f64,
    pub video_rate_independent: bool,
    pub video_rate_multiplier: f64,

    pub peak_rate_enabled: bool,
    pub peak_start: String,
    pub peak_end: String,
    pub peak_rate_multiplier: f64,
    pub image_price_1k: Option<f64>,
    pub image_price_2k: Option<f64>,
    pub image_price_4k: Option<f64>,
    pub video_price_480p: Option<f64>,
    pub video_price_720p: Option<f64>,
    pub video_price_1080p: Option<f64>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub video_model_prices: BTreeMap<String, BTreeMap<String, f64>>,
    pub web_search_price_per_call: Option<f64>,
    pub search_price_per_1k: Option<f64>,
    pub audio_realtime_price_per_min: Option<f64>,
    pub audio_tts_price_per_million_chars: Option<f64>,
    pub audio_stt_price_per_hour: Option<f64>,

    pub claude_code_only: bool,
    pub fallback_group_id: Option<i64>,
    pub fallback_group_id_on_invalid_request: Option<i64>,

    pub allow_messages_dispatch: bool,
    pub allow_live: bool,

    pub require_oauth_only: bool,
    pub require_privacy_set: bool,

    pub rpm_limit: i32,
    pub max_reasoning_effort: String,
    pub max_reasoning_effort_over_limit: String,
    /// 无 omitempty：即使为空数组也会输出 `[]`。
    pub reasoning_effort_mappings: Vec<ReasoningEffortMapping>,

    pub created_at: DateTime<Utc>,
    pub updated_at: DateTime<Utc>,
}

/// API Key DTO，字段与 Go 版 `dto.APIKey` 逐一对齐（含省略行为）。
///
/// ⚠️ `key` 为**明文密钥**（Go 版后端不做脱敏，脱敏由前端
/// `frontend/src/utils/maskApiKey.ts` 负责）。这一点必须原样保持，
/// 否则前端展示会与 Go 版不一致。
#[derive(Debug, Clone, Serialize)]
pub struct ApiKeyDto {
    pub id: i64,
    pub user_id: i64,
    pub key: String,
    pub name: String,
    pub group_id: Option<i64>,
    pub fallback_group_id: Option<i64>,
    pub status: String,
    /// 无 omitempty：nil 时输出 `null`（与 Go 的 `[]string` 行为一致）。
    pub ip_whitelist: Option<Vec<String>>,
    pub ip_blacklist: Option<Vec<String>>,
    pub last_used_at: Option<DateTime<Utc>>,
    pub last_used_ip: Option<String>,
    pub quota: f64,
    pub quota_used: f64,
    pub expires_at: Option<DateTime<Utc>>,
    pub created_at: DateTime<Utc>,
    pub updated_at: DateTime<Utc>,
    pub current_concurrency: i32,

    pub rate_limit_5h: f64,
    pub rate_limit_1d: f64,
    pub rate_limit_7d: f64,
    pub usage_5h: f64,
    pub usage_1d: f64,
    pub usage_7d: f64,
    pub window_5h_start: Option<DateTime<Utc>>,
    pub window_1d_start: Option<DateTime<Utc>>,
    pub window_7d_start: Option<DateTime<Utc>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub reset_5h_at: Option<DateTime<Utc>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub reset_1d_at: Option<DateTime<Utc>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub reset_7d_at: Option<DateTime<Utc>>,

    /// `ListByUserID` 不预加载用户，故通常为 `None`（省略）。
    #[serde(skip_serializing_if = "Option::is_none")]
    pub user: Option<serde_json::Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub group: Option<GroupDto>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub fallback_group: Option<GroupDto>,
}

/// 构造 API Key DTO，对应 Go 版 `APIKeyFromService`。
///
/// `now` 由调用方传入，便于测试固定时钟。
pub fn build_api_key_dto(
    key: &ApiKeyRecord,
    group: Option<GroupDto>,
    fallback_group: Option<GroupDto>,
    now: DateTime<Utc>,
) -> ApiKeyDto {
    ApiKeyDto {
        id: key.id,
        user_id: key.user_id,
        key: key.key.clone(),
        name: key.name.clone(),
        group_id: key.group_id,
        fallback_group_id: key.fallback_group_id,
        status: key.status.clone(),
        ip_whitelist: key.ip_whitelist.clone(),
        ip_blacklist: key.ip_blacklist.clone(),
        last_used_at: key.last_used_at,
        last_used_ip: key.last_used_ip.clone(),
        quota: key.quota,
        quota_used: key.quota_used,
        expires_at: key.expires_at,
        created_at: key.created_at,
        updated_at: key.updated_at,
        // 实时并发数由网关侧维护，列表接口不返回实时值（Go 同样为 0）。
        current_concurrency: 0,
        rate_limit_5h: key.rate_limit_5h,
        rate_limit_1d: key.rate_limit_1d,
        rate_limit_7d: key.rate_limit_7d,
        // 用「有效用量」而非原始值：窗口过期按 0 计。
        usage_5h: effective_usage(key.usage_5h, key.window_5h_start, RATE_LIMIT_WINDOW_5H, now),
        usage_1d: effective_usage(key.usage_1d, key.window_1d_start, RATE_LIMIT_WINDOW_1D, now),
        usage_7d: effective_usage(key.usage_7d, key.window_7d_start, RATE_LIMIT_WINDOW_7D, now),
        window_5h_start: key.window_5h_start,
        window_1d_start: key.window_1d_start,
        window_7d_start: key.window_7d_start,
        reset_5h_at: reset_at(key.window_5h_start, RATE_LIMIT_WINDOW_5H, now),
        reset_1d_at: reset_at(key.window_1d_start, RATE_LIMIT_WINDOW_1D, now),
        reset_7d_at: reset_at(key.window_7d_start, RATE_LIMIT_WINDOW_7D, now),
        user: None,
        group,
        fallback_group,
    }
}

/// API Key 的数据库记录（本模块所需子集）。
#[derive(Debug, Clone)]
pub struct ApiKeyRecord {
    pub id: i64,
    pub user_id: i64,
    pub key: String,
    pub name: String,
    pub group_id: Option<i64>,
    pub fallback_group_id: Option<i64>,
    pub status: String,
    pub ip_whitelist: Option<Vec<String>>,
    pub ip_blacklist: Option<Vec<String>>,
    pub last_used_at: Option<DateTime<Utc>>,
    pub last_used_ip: Option<String>,
    pub quota: f64,
    pub quota_used: f64,
    pub expires_at: Option<DateTime<Utc>>,
    pub created_at: DateTime<Utc>,
    pub updated_at: DateTime<Utc>,
    pub rate_limit_5h: f64,
    pub rate_limit_1d: f64,
    pub rate_limit_7d: f64,
    pub usage_5h: f64,
    pub usage_1d: f64,
    pub usage_7d: f64,
    pub window_5h_start: Option<DateTime<Utc>>,
    pub window_1d_start: Option<DateTime<Utc>>,
    pub window_7d_start: Option<DateTime<Utc>>,
}

/// 分页参数，对应 Go 版 `pagination.PaginationParams` + `response.ParsePagination`。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Pagination {
    pub page: i64,
    pub page_size: i64,
}

impl Default for Pagination {
    fn default() -> Self {
        Self {
            page: 1,
            page_size: 20,
        }
    }
}

/// 解析分页参数，对应 Go 版 `response.ParsePagination`。
///
/// 规则（与 Go 完全一致）：
/// - `page` 缺省/非法/≤0 → 1
/// - `page_size` 优先，其次 `limit`；非法/≤0/>1000 → 20
pub fn parse_pagination(
    page: Option<&str>,
    page_size: Option<&str>,
    limit: Option<&str>,
) -> Pagination {
    let mut out = Pagination::default();

    if let Some(v) = page.and_then(parse_positive_int) {
        if v > 0 {
            out.page = v;
        }
    }

    let size = page_size.or(limit).and_then(parse_positive_int);
    if let Some(v) = size {
        if v > 0 && v <= 1000 {
            out.page_size = v;
        }
    }

    out
}

/// 仅接受纯数字字符串（对应 Go 版 `parseInt` 的「遇非数字即返回 0」行为）。
fn parse_positive_int(s: &str) -> Option<i64> {
    let t = s.trim();
    if t.is_empty() || !t.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    t.parse::<i64>().ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ts(s: &str) -> DateTime<Utc> {
        s.parse().unwrap()
    }

    fn sample_key() -> ApiKeyRecord {
        ApiKeyRecord {
            id: 92,
            user_id: 46,
            key: "sk-roundtrip-key".to_string(),
            name: "my-key".to_string(),
            group_id: Some(3),
            fallback_group_id: None,
            status: "active".to_string(),
            ip_whitelist: None,
            ip_blacklist: None,
            last_used_at: None,
            last_used_ip: None,
            quota: 0.0,
            quota_used: 1.5,
            expires_at: None,
            created_at: ts("2026-01-01T00:00:00Z"),
            updated_at: ts("2026-01-02T00:00:00Z"),
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

    // ── 窗口语义 ──

    #[test]
    fn nil_window_is_expired() {
        assert!(is_window_expired(None, RATE_LIMIT_WINDOW_5H, Utc::now()));
    }

    #[test]
    fn window_not_expired_before_duration() {
        let now = ts("2026-06-01T12:00:00Z");
        let start = ts("2026-06-01T08:00:00Z"); // 4 小时前 < 5 小时
        assert!(!is_window_expired(Some(start), RATE_LIMIT_WINDOW_5H, now));
    }

    /// 边界：恰好等于窗口时长即视为过期（Go 用 `>=`）。
    #[test]
    fn window_expired_at_exact_boundary() {
        let now = ts("2026-06-01T13:00:00Z");
        let start = ts("2026-06-01T08:00:00Z"); // 恰好 5 小时
        assert!(is_window_expired(Some(start), RATE_LIMIT_WINDOW_5H, now));
    }

    #[test]
    fn effective_usage_zero_when_expired() {
        let now = ts("2026-06-02T00:00:00Z");
        let start = ts("2026-06-01T00:00:00Z"); // 24 小时前 → 5h 窗口已过期
        assert_eq!(
            effective_usage(12.5, Some(start), RATE_LIMIT_WINDOW_5H, now),
            0.0
        );
    }

    #[test]
    fn effective_usage_keeps_value_when_active() {
        let now = ts("2026-06-01T02:00:00Z");
        let start = ts("2026-06-01T00:00:00Z");
        assert_eq!(
            effective_usage(12.5, Some(start), RATE_LIMIT_WINDOW_5H, now),
            12.5
        );
    }

    #[test]
    fn reset_at_none_when_expired_or_missing() {
        let now = ts("2026-06-02T00:00:00Z");
        assert!(reset_at(None, RATE_LIMIT_WINDOW_5H, now).is_none());
        let stale = ts("2026-06-01T00:00:00Z");
        assert!(reset_at(Some(stale), RATE_LIMIT_WINDOW_5H, now).is_none());
    }

    #[test]
    fn reset_at_is_start_plus_window() {
        let now = ts("2026-06-01T02:00:00Z");
        let start = ts("2026-06-01T00:00:00Z");
        assert_eq!(
            reset_at(Some(start), RATE_LIMIT_WINDOW_5H, now),
            Some(ts("2026-06-01T05:00:00Z"))
        );
    }

    // ── DTO 映射 ──

    /// 明文密钥必须原样输出（脱敏是前端的职责）。
    #[test]
    fn dto_exposes_plaintext_key() {
        let dto = build_api_key_dto(&sample_key(), None, None, Utc::now());
        let v = serde_json::to_value(&dto).unwrap();
        assert_eq!(v["key"], "sk-roundtrip-key");
    }

    /// nil 的 ip 列表输出 `null`（无 omitempty），与 Go 的 `[]string` 一致。
    #[test]
    fn nil_ip_lists_serialize_as_null() {
        let dto = build_api_key_dto(&sample_key(), None, None, Utc::now());
        let v = serde_json::to_value(&dto).unwrap();
        assert!(v["ip_whitelist"].is_null());
        assert!(v["ip_blacklist"].is_null());
    }

    /// 未预加载的 group 字段应被省略（omitempty 指针）。
    #[test]
    fn absent_group_fields_are_omitted() {
        let dto = build_api_key_dto(&sample_key(), None, None, Utc::now());
        let v = serde_json::to_value(&dto).unwrap();
        assert!(v.get("group").is_none());
        assert!(v.get("fallback_group").is_none());
        assert!(v.get("user").is_none());
    }

    /// reset_* 为空时应省略。
    #[test]
    fn reset_fields_omitted_when_none() {
        let dto = build_api_key_dto(&sample_key(), None, None, Utc::now());
        let v = serde_json::to_value(&dto).unwrap();
        for k in ["reset_5h_at", "reset_1d_at", "reset_7d_at"] {
            assert!(v.get(k).is_none(), "{k} 应被省略");
        }
    }

    /// group_id 为 None 时输出 null（无 omitempty）。
    #[test]
    fn null_group_id_serialized_as_null() {
        let mut k = sample_key();
        k.group_id = None;
        let dto = build_api_key_dto(&k, None, None, Utc::now());
        let v = serde_json::to_value(&dto).unwrap();
        assert!(v["group_id"].is_null());
        assert_eq!(v["fallback_group_id"], serde_json::Value::Null);
    }

    // ── 分页 ──

    #[test]
    fn pagination_defaults() {
        let p = parse_pagination(None, None, None);
        assert_eq!(p.page, 1);
        assert_eq!(p.page_size, 20);
    }

    #[test]
    fn pagination_reads_values() {
        let p = parse_pagination(Some("3"), Some("50"), None);
        assert_eq!(p.page, 3);
        assert_eq!(p.page_size, 50);
    }

    /// `limit` 作为 `page_size` 的备选名（与 Go 一致）。
    #[test]
    fn pagination_falls_back_to_limit() {
        let p = parse_pagination(None, None, Some("7"));
        assert_eq!(p.page_size, 7);
    }

    /// page_size 优先于 limit。
    #[test]
    fn page_size_takes_precedence_over_limit() {
        let p = parse_pagination(None, Some("30"), Some("7"));
        assert_eq!(p.page_size, 30);
    }

    /// 超过 1000 的上限被忽略，回退默认值。
    #[test]
    fn page_size_over_limit_falls_back() {
        let p = parse_pagination(None, Some("1001"), None);
        assert_eq!(p.page_size, 20);
    }

    /// 非数字/零/负数被忽略。
    #[test]
    fn invalid_pagination_values_ignored() {
        assert_eq!(parse_pagination(Some("abc"), None, None).page, 1);
        assert_eq!(parse_pagination(Some("0"), None, None).page, 1);
        assert_eq!(parse_pagination(Some("-5"), None, None).page, 1);
        assert_eq!(parse_pagination(None, Some("0"), None).page_size, 20);
        // Go 的 parseInt 遇到小数点会返回 0（非纯数字）。
        assert_eq!(parse_pagination(Some("1.5"), None, None).page, 1);
    }

    // ── 嵌套 JSON 字段 ──

    #[test]
    fn group_omits_empty_json_collections() {
        let g = GroupDto {
            id: 1,
            name: "g".into(),
            description: String::new(),
            platform: "openai".into(),
            rate_multiplier: 1.0,
            model_rate_multipliers: vec![],
            is_exclusive: false,
            status: "active".into(),
            subscription_type: String::new(),
            daily_limit_usd: None,
            weekly_limit_usd: None,
            monthly_limit_usd: None,
            long_context_pricing_enabled: false,
            allow_image_generation: false,
            allow_batch_image_generation: false,
            image_rate_independent: false,
            image_rate_multiplier: 0.0,
            batch_image_discount_multiplier: 0.0,
            batch_image_hold_multiplier: 0.0,
            video_rate_independent: false,
            video_rate_multiplier: 0.0,
            peak_rate_enabled: false,
            peak_start: String::new(),
            peak_end: String::new(),
            peak_rate_multiplier: 0.0,
            image_price_1k: None,
            image_price_2k: None,
            image_price_4k: None,
            video_price_480p: None,
            video_price_720p: None,
            video_price_1080p: None,
            video_model_prices: BTreeMap::new(),
            web_search_price_per_call: None,
            search_price_per_1k: None,
            audio_realtime_price_per_min: None,
            audio_tts_price_per_million_chars: None,
            audio_stt_price_per_hour: None,
            claude_code_only: false,
            fallback_group_id: None,
            fallback_group_id_on_invalid_request: None,
            allow_messages_dispatch: false,
            allow_live: false,
            require_oauth_only: false,
            require_privacy_set: false,
            rpm_limit: 0,
            max_reasoning_effort: String::new(),
            max_reasoning_effort_over_limit: String::new(),
            reasoning_effort_mappings: vec![],
            created_at: ts("2026-01-01T00:00:00Z"),
            updated_at: ts("2026-01-01T00:00:00Z"),
        };
        let v = serde_json::to_value(&g).unwrap();
        // omitempty 的三项应省略
        assert!(v.get("model_rate_multipliers").is_none());
        assert!(v.get("video_model_prices").is_none());
        // 无 omitempty 的数组应输出 []
        assert_eq!(v["reasoning_effort_mappings"], serde_json::json!([]));
        // 无 omitempty 的指针应输出 null
        assert!(v["daily_limit_usd"].is_null());
    }

    /// 倍率规则：hidden=false 时省略 hidden 字段（对应 Go 的 omitempty）。
    #[test]
    fn multiplier_rule_omits_false_hidden() {
        let r = ModelRateMultiplierRule {
            model: "gpt-5".into(),
            multiplier: 2.0,
            hidden: false,
        };
        let v = serde_json::to_value(&r).unwrap();
        assert_eq!(v, serde_json::json!({"model": "gpt-5", "multiplier": 2.0}));

        let r2 = ModelRateMultiplierRule {
            model: "gpt-5".into(),
            multiplier: 2.0,
            hidden: true,
        };
        let v2 = serde_json::to_value(&r2).unwrap();
        assert_eq!(v2["hidden"], true);
    }
}
