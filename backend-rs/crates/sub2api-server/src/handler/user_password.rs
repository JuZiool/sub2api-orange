//! 改密相关的请求/响应与校验。
//!
//! 对齐 Go 版：
//! - Handler `UserHandler.ChangePassword`（`internal/handler/user_handler.go`）
//! - 请求体 `ChangePasswordRequest`：`old_password` 必填、`new_password` 必填且 `min=6`
//! - 服务 `UserService.ChangePassword`（`internal/service/user_service.go`）
//!
//! ## 端点契约
//!
//! - 请求：`PUT /api/v1/user/password`，JSON `{old_password, new_password}`
//! - 成功：HTTP 200，业务信封 `{"code":0,"message":"success","data":{"message":"Password changed successfully"}}`
//! - 字段缺失：HTTP 400，业务信封 `{"code":400,"message":"Invalid request: ..."}`
//! - 当前密码错误：HTTP 400，`{"code":400,"reason":"PASSWORD_INCORRECT","message":"current password is incorrect"}`
//!
//! ## 为何改密能使旧 token 失效
//!
//! `users` 表**没有** `token_version` 列。TokenVersion 是由
//! `email + password_hash` 派生的指纹（见 `sub2api_auth::token_version`）。
//! 改密写入新的 `password_hash` 后，指纹随之改变，
//! 中间件的第 7 步校验（`claims.token_version != user.token_version`）即会
//! 拒绝所有旧 token（响应 `401 TOKEN_REVOKED`）。

use serde::{Deserialize, Serialize};

/// 新密码最小长度，对应 Go 版 `binding:"required,min=6"`。
pub const MIN_NEW_PASSWORD_LEN: usize = 6;

/// 改密请求体，字段名对齐 Go 版 `ChangePasswordRequest`。
#[derive(Debug, Clone, Deserialize)]
pub struct ChangePasswordRequest {
    /// 当前密码。
    #[serde(default)]
    pub old_password: String,
    /// 新密码。
    #[serde(default)]
    pub new_password: String,
}

/// 请求校验结果，对应 Go 版 `binding` 标签的语义。
#[derive(Debug, PartialEq, Eq)]
pub enum ValidationError {
    /// `old_password` 缺失或为空。
    OldPasswordRequired,
    /// `new_password` 缺失或为空。
    NewPasswordRequired,
    /// `new_password` 长度不足。
    NewPasswordTooShort { min: usize },
}

impl ValidationError {
    /// 生成与 Go 版 gin 绑定错误风格接近的提示文案。
    ///
    /// 注：Go 版直接拼接 gin 的绑定错误文本（如
    /// `Key: 'ChangePasswordRequest.NewPassword' Error:Field validation for ...`）。
    /// 这里给出等价的要点式文案——前端仅做提示展示，不依赖具体措辞。
    pub fn message(&self) -> String {
        match self {
            ValidationError::OldPasswordRequired => {
                "Key: 'ChangePasswordRequest.OldPassword' Error:Field validation for 'OldPassword' failed on the 'required' tag".to_string()
            }
            ValidationError::NewPasswordRequired => {
                "Key: 'ChangePasswordRequest.NewPassword' Error:Field validation for 'NewPassword' failed on the 'required' tag".to_string()
            }
            ValidationError::NewPasswordTooShort { min } => format!(
                "Key: 'ChangePasswordRequest.NewPassword' Error:Field validation for 'NewPassword' failed on the 'min' tag (min={min})"
            ),
        }
    }
}

impl ChangePasswordRequest {
    /// 校验请求，对应 Go 的 `binding:"required"` / `binding:"required,min=6"`。
    ///
    /// 校验顺序与 gin 一致：先生成 `old_password`，再校验 `new_password`。
    pub fn validate(&self) -> Result<(), ValidationError> {
        if self.old_password.is_empty() {
            return Err(ValidationError::OldPasswordRequired);
        }
        if self.new_password.is_empty() {
            return Err(ValidationError::NewPasswordRequired);
        }
        // 按**字符**计数（面向用户可见长度），与常见前端校验口径一致。
        if self.new_password.chars().count() < MIN_NEW_PASSWORD_LEN {
            return Err(ValidationError::NewPasswordTooShort {
                min: MIN_NEW_PASSWORD_LEN,
            });
        }
        Ok(())
    }
}

/// 改密成功响应体，对应 Go 版 `gin.H{"message": "Password changed successfully"}`。
#[derive(Debug, Clone, Serialize)]
pub struct ChangePasswordResponse {
    pub message: String,
}

impl Default for ChangePasswordResponse {
    fn default() -> Self {
        Self {
            message: "Password changed successfully".to_string(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn req(old: &str, new: &str) -> ChangePasswordRequest {
        ChangePasswordRequest {
            old_password: old.to_string(),
            new_password: new.to_string(),
        }
    }

    #[test]
    fn valid_request_passes() {
        assert!(req("old-pass", "new-pass-123").validate().is_ok());
    }

    #[test]
    fn empty_old_password_rejected() {
        assert_eq!(
            req("", "new-pass-123").validate(),
            Err(ValidationError::OldPasswordRequired)
        );
    }

    #[test]
    fn empty_new_password_rejected() {
        assert_eq!(
            req("old-pass", "").validate(),
            Err(ValidationError::NewPasswordRequired)
        );
    }

    /// 恰好 6 个字符应通过（对齐 Go 的 `min=6` 含边界）。
    #[test]
    fn exactly_min_length_passes() {
        assert!(req("old", "abcdef").validate().is_ok());
    }

    #[test]
    fn below_min_length_rejected() {
        assert_eq!(
            req("old", "abcde").validate(),
            Err(ValidationError::NewPasswordTooShort { min: 6 })
        );
    }

    /// 长度按字符而非字节：3 个中文（9 字节）仍不足 6。
    #[test]
    fn length_counted_in_characters_not_bytes() {
        assert!(
            req("old", "中文密码测试").validate().is_ok(),
            "6 个中文字符应通过"
        );
        assert_eq!(
            req("old", "中文密").validate(),
            Err(ValidationError::NewPasswordTooShort { min: 6 })
        );
    }

    /// 校验顺序：old 为空时优先报 old 的问题。
    #[test]
    fn old_password_checked_first() {
        assert_eq!(
            req("", "").validate(),
            Err(ValidationError::OldPasswordRequired)
        );
    }

    /// 反序列化：缺字段时用默认空串（不因缺字段直接反序列化失败）。
    #[test]
    fn deserializes_with_missing_fields() {
        let r: ChangePasswordRequest = serde_json::from_str("{}").unwrap();
        assert!(r.old_password.is_empty());
        assert!(r.new_password.is_empty());
        assert!(r.validate().is_err());
    }

    #[test]
    fn deserializes_go_field_names() {
        let r: ChangePasswordRequest =
            serde_json::from_str(r#"{"old_password":"o","new_password":"n12345"}"#).unwrap();
        assert_eq!(r.old_password, "o");
        assert_eq!(r.new_password, "n12345");
        assert!(r.validate().is_ok());
    }

    #[test]
    fn response_message_matches_go() {
        let v = serde_json::to_value(ChangePasswordResponse::default()).unwrap();
        assert_eq!(
            v,
            serde_json::json!({"message": "Password changed successfully"})
        );
    }
}
