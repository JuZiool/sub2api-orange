//! 标准 API 响应信封。
//!
//! 与 Go 版 `internal/pkg/response/response.go` 的 JSON 结构**逐字段对齐**：
//!
//! | 字段 | 类型 | 省略条件 |
//! |---|---|---|
//! | `code` | int | 不省略。成功为 0，失败为 HTTP 状态码 |
//! | `message` | string | 不省略。成功为 `success` / `accepted` |
//! | `reason` | string | 空字符串时省略 |
//! | `metadata` | object | `null` 时省略 |
//! | `data` | any | `null` 时省略 |
//!
//! 前端依赖这个结构解析所有 `/api/v1/**` 响应，因此字段名与省略行为不能变。
//!
//! 示例：
//! ```json
//! {"code":0,"message":"success","data":{"items":[],"total":0,"page":1,"page_size":20,"pages":1}}
//! {"code":500,"message":"Internal Server Error"}
//! ```

// 本模块是契约层：完整的便捷构造函数与信封类型为后续期次的全部 handler 服务。
// 期 1 只有少数端点使用，因此对未使用项整体放行，避免 dead_code 噪音掩盖真实问题。
#![allow(dead_code)]

use std::collections::BTreeMap;

use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::Json;
use serde::Serialize;
use serde_json::Value;

/// 标准响应信封。字段省略行为与 Go 版 `Response` 结构体的 json tag 一致。
#[derive(Debug, Clone, Serialize)]
pub struct ApiResponse {
    pub code: i32,
    pub message: String,
    /// 对齐 Go 的 `json:"reason,omitempty"`。
    #[serde(skip_serializing_if = "String::is_empty")]
    pub reason: String,
    /// 对齐 Go 的 `json:"metadata,omitempty"`（nil map 会省略）。
    #[serde(skip_serializing_if = "Option::is_none")]
    pub metadata: Option<BTreeMap<String, String>>,
    /// 对齐 Go 的 `json:"data,omitempty"`（nil 会省略）。
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<Value>,
}

/// 分页数据格式，字段与 Go 版 `PaginatedData` 一致（注意是 `page_size` 下划线）。
#[derive(Debug, Clone, Serialize)]
pub struct PaginatedData {
    pub items: Value,
    pub total: i64,
    pub page: i64,
    pub page_size: i64,
    pub pages: i64,
}

impl PaginatedData {
    /// 构造分页体，`pages` 计算与 Go 版 `Paginated` 一致（向上取整，最小 1）。
    pub fn new(items: Value, total: i64, page: i64, page_size: i64) -> Self {
        let pages = if page_size > 0 {
            ((total + page_size - 1) / page_size).max(1)
        } else {
            1
        };
        Self {
            items,
            total,
            page,
            page_size,
            pages,
        }
    }
}

impl ApiResponse {
    /// 成功响应（HTTP 200，code 0，message `success`）。
    pub fn success(data: Option<Value>) -> Self {
        Self {
            code: 0,
            message: "success".to_string(),
            reason: String::new(),
            metadata: None,
            data,
        }
    }

    /// 创建成功响应（HTTP 201，code 0，message `success`）。
    pub fn created(data: Option<Value>) -> Self {
        Self::success(data)
    }

    /// 异步接受响应（HTTP 202，code 0，message `accepted`）。
    pub fn accepted(data: Option<Value>) -> Self {
        Self {
            code: 0,
            message: "accepted".to_string(),
            reason: String::new(),
            metadata: None,
            data,
        }
    }

    /// 错误响应。`code` 等于 HTTP 状态码，与 Go 版 `Error` 一致。
    pub fn error(status: StatusCode, message: impl Into<String>) -> Self {
        Self {
            code: status.as_u16() as i32,
            message: message.into(),
            reason: String::new(),
            metadata: None,
            data: None,
        }
    }

    /// 带结构化详情的错误响应，对应 Go 版 `ErrorWithDetails`。
    pub fn error_with_details(
        status: StatusCode,
        message: impl Into<String>,
        reason: impl Into<String>,
        metadata: Option<BTreeMap<String, String>>,
    ) -> Self {
        Self {
            code: status.as_u16() as i32,
            message: message.into(),
            reason: reason.into(),
            metadata,
            data: None,
        }
    }

    /// 分页成功响应，对应 Go 版 `Paginated`。
    ///
    /// `pages` 计算方式与 Go 版一致：向上取整，且最小为 1。
    pub fn paginated(items: Value, total: i64, page: i64, page_size: i64) -> Self {
        let pages = if page_size > 0 {
            // 向上取整；`i64::div_ceil` 尚未稳定，手工计算。
            ((total + page_size - 1) / page_size).max(1)
        } else {
            1
        };

        Self::success(Some(
            serde_json::to_value(PaginatedData {
                items,
                total,
                page,
                page_size,
                pages,
            })
            .unwrap_or(Value::Null),
        ))
    }
}

/// 便捷构造：成功响应（200）。
pub fn success(data: impl Into<Value>) -> (StatusCode, Json<ApiResponse>) {
    (
        StatusCode::OK,
        Json(ApiResponse::success(Some(data.into()))),
    )
}

/// 便捷构造：无数据成功响应。
pub fn ok() -> (StatusCode, Json<ApiResponse>) {
    (StatusCode::OK, Json(ApiResponse::success(None)))
}

/// 便捷构造：错误响应。
pub fn error(status: StatusCode, message: impl Into<String>) -> Response {
    (status, Json(ApiResponse::error(status, message))).into_response()
}

/// 便捷构造：400。
pub fn bad_request(message: impl Into<String>) -> Response {
    error(StatusCode::BAD_REQUEST, message)
}

/// 便捷构造：401。
pub fn unauthorized(message: impl Into<String>) -> Response {
    error(StatusCode::UNAUTHORIZED, message)
}

/// 便捷构造：403。
pub fn forbidden(message: impl Into<String>) -> Response {
    error(StatusCode::FORBIDDEN, message)
}

/// 便捷构造：404。
pub fn not_found(message: impl Into<String>) -> Response {
    error(StatusCode::NOT_FOUND, message)
}

/// 便捷构造：500。
pub fn internal_error(message: impl Into<String>) -> Response {
    error(StatusCode::INTERNAL_SERVER_ERROR, message)
}

/// 业务错误体，对应 Go 版 `infraerrors.Status`。
///
/// ⚠️ 注意与 [`ApiResponse`] 的差别：这里是 **`code` = HTTP 状态码**、
/// **`reason` = 机器可读错误码**（如 `PASSWORD_INCORRECT`）。
/// 而 [`ApiResponse`] 在成功时 `code` 为 0。
///
/// 字段顺序遵循 Go 的 `Status` 结构体：`code` / `reason` / `message` / `metadata`。
#[derive(Debug, Clone, Serialize)]
pub struct BusinessError {
    pub code: i32,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub reason: String,
    pub message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub metadata: Option<std::collections::BTreeMap<String, String>>,
}

impl BusinessError {
    /// 构造业务错误，对应 Go 版 `infraerrors.New(code, reason, message)`。
    pub fn new(status: StatusCode, reason: impl Into<String>, message: impl Into<String>) -> Self {
        Self {
            code: status.as_u16() as i32,
            reason: reason.into(),
            message: message.into(),
            metadata: None,
        }
    }

    /// 转成完整响应，对应 Go 版 `response.ErrorFrom` 的输出。
    pub fn into_response_with_status(self, status: StatusCode) -> Response {
        (status, Json(self)).into_response()
    }
}

/// 对应 Go 版 `infraerrors.BadRequest(reason, message)`（HTTP 400）。
pub fn bad_request_with_reason(reason: impl Into<String>, message: impl Into<String>) -> Response {
    BusinessError::new(StatusCode::BAD_REQUEST, reason, message)
        .into_response_with_status(StatusCode::BAD_REQUEST)
}

/// 对应 Go 版 `infraerrors.Unauthorized(reason, message)`（HTTP 401）。
pub fn unauthorized_with_reason(reason: impl Into<String>, message: impl Into<String>) -> Response {
    BusinessError::new(StatusCode::UNAUTHORIZED, reason, message)
        .into_response_with_status(StatusCode::UNAUTHORIZED)
}

/// 对应 Go 版 `infraerrors.NotFound(reason, message)`（HTTP 404）。
pub fn not_found_with_reason(reason: impl Into<String>, message: impl Into<String>) -> Response {
    BusinessError::new(StatusCode::NOT_FOUND, reason, message)
        .into_response_with_status(StatusCode::NOT_FOUND)
}

/// 业务错误码常量，取值与 Go 版逐字一致。
pub mod business_codes {
    /// 当前密码不正确（HTTP 400）。
    pub const PASSWORD_INCORRECT: &str = "PASSWORD_INCORRECT";
    /// 用户不存在（HTTP 404）。
    pub const USER_NOT_FOUND: &str = "USER_NOT_FOUND";
    /// token 已过期（HTTP 401）。
    pub const TOKEN_EXPIRED: &str = "TOKEN_EXPIRED";
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    /// 与 Go 版 `Success(c, nil)` 输出一致：data 省略。
    #[test]
    fn success_without_data_omits_data_field() {
        let resp = ApiResponse::success(None);
        let v = serde_json::to_value(&resp).unwrap();
        assert_eq!(v, json!({"code": 0, "message": "success"}));
    }

    /// 与 Go 版 `Success(c, data)` 输出一致。
    #[test]
    fn success_with_data_includes_payload() {
        let resp = ApiResponse::success(Some(json!({"id": 1})));
        let v = serde_json::to_value(&resp).unwrap();
        assert_eq!(
            v,
            json!({"code": 0, "message": "success", "data": {"id": 1}})
        );
    }

    /// 与 Go 版 `Error(c, 401, "x")` 一致：reason/metadata/data 全部省略。
    #[test]
    fn error_envelope_matches_go() {
        let resp = ApiResponse::error(StatusCode::UNAUTHORIZED, "unauthorized");
        let v = serde_json::to_value(&resp).unwrap();
        assert_eq!(v, json!({"code": 401, "message": "unauthorized"}));
    }

    /// reason 为空时省略，非空时保留。
    #[test]
    fn error_with_details_includes_reason_and_metadata() {
        let mut md = BTreeMap::new();
        md.insert("k".to_string(), "v".to_string());
        let resp = ApiResponse::error_with_details(
            StatusCode::BAD_REQUEST,
            "bad",
            "invalid_input",
            Some(md),
        );
        let v = serde_json::to_value(&resp).unwrap();
        assert_eq!(
            v,
            json!({"code": 400, "message": "bad", "reason": "invalid_input", "metadata": {"k": "v"}})
        );
    }

    /// 分页 pages 向上取整，与 Go 版 math.Ceil 行为一致。
    #[test]
    fn paginated_rounds_pages_up() {
        let resp = ApiResponse::paginated(json!([]), 21, 1, 20);
        let v = serde_json::to_value(&resp).unwrap();
        assert_eq!(v["data"]["pages"], json!(2));
        assert_eq!(v["data"]["total"], json!(21));
        assert_eq!(v["data"]["page_size"], json!(20));
    }

    /// total 为 0 时 pages 最小为 1（Go 版同样处理）。
    #[test]
    fn paginated_minimum_one_page() {
        let resp = ApiResponse::paginated(json!([]), 0, 1, 20);
        let v = serde_json::to_value(&resp).unwrap();
        assert_eq!(v["data"]["pages"], json!(1));
    }

    /// 业务错误：`code` 为 HTTP 状态码，`reason` 为机器码，空字段省略。
    /// 对应 Go 版 `infraerrors.BadRequest("PASSWORD_INCORRECT", "...")` 的输出。
    #[test]
    fn business_error_matches_go_status_shape() {
        let e = BusinessError::new(
            StatusCode::BAD_REQUEST,
            business_codes::PASSWORD_INCORRECT,
            "current password is incorrect",
        );
        let v = serde_json::to_value(&e).unwrap();
        assert_eq!(
            v,
            json!({
                "code": 400,
                "reason": "PASSWORD_INCORRECT",
                "message": "current password is incorrect"
            })
        );
        // 关键差异：业务错误的 code 是 HTTP 状态码（400），不是 0。
        assert_eq!(v["code"], 400);
        assert!(v.get("metadata").is_none(), "metadata 为空时应省略");
    }

    /// 业务错误与成功信封是不同的结构，不能混用。
    #[test]
    fn business_error_differs_from_api_response() {
        let success = serde_json::to_value(ApiResponse::success(None)).unwrap();
        let failure =
            serde_json::to_value(BusinessError::new(StatusCode::BAD_REQUEST, "REASON", "msg"))
                .unwrap();

        assert_eq!(success["code"], 0);
        assert_eq!(failure["code"], 400);
        assert!(success.get("reason").is_none());
        assert_eq!(failure["reason"], "REASON");
    }
}
