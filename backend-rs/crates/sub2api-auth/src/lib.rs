//! 认证模块。
//!
//! 承载 JWT 签发/校验、TokenVersion 指纹派生、认证中间件与认证错误信封。
//! 目标：与 Go 版 `internal/service/auth_service.go` 及
//! `internal/server/middleware/jwt_auth.go` 的行为保持一致。
//!
//! ## 关键契约
//!
//! - **TokenVersion 是派生指纹**，不是数据库列：由 `email + password_hash`
//!   经 SHA256 派生。改密即自动撤销旧 token。算法必须与 Go 逐位一致
//!   （见 [`token_version`]，其测试使用 Go 实际运行产出的对照向量）。
//! - **认证错误信封与业务信封不同**：中间件用 [`errors::ErrorResponse`]
//!   （`code` 为字符串），业务 handler 用整数 `code`。见 [`errors`] 的说明。

pub mod errors;
pub mod identity;
pub mod jwt;
pub mod jwt_auth;
pub mod password;
pub mod token_version;

pub use errors::{abort_with_error, codes, ErrorResponse};
pub use identity::{
    build_identity_summary_set, IdentityRecord, IdentitySummary, IdentitySummarySet,
};
pub use jwt::{access_token_expiry, encode_access_token, validate_token, JwtClaims, TokenError};
pub use jwt_auth::{AuthFailure, AuthUser, JwtAuthState, LoadOutcome, UserDirectory};
pub use password::{hash_password, verify_password, BCRYPT_COST};
pub use token_version::resolved_token_version;
