//! JWT 签发与校验。
//!
//! 逐条对齐 Go 版 `internal/service/auth_service.go` 的 `GenerateToken` /
//! `ValidateToken`：
//!
//! | 项 | Go 版取值 |
//! |---|---|
//! | 签名算法（签发） | `HS256` |
//! | 允许校验的算法 | `HS256` / `HS384` / `HS512`（防算法混淆） |
//! | 密钥 | `jwt.secret`（原始字节，非 base64 解码） |
//! | 长度上限 | 8192 字节，超出立即拒绝（降低 DoS 风险） |
//! | 标准声明 | `exp` / `iat` / `nbf` |
//! | 自定义声明 | `user_id` / `email` / `role` / `token_version` / `sid?` / `bnd?` |
//!
//! ## 过期时仍返回 claims
//!
//! Go 版在 token 过期时**返回 claims 且同时返回 `ErrTokenExpired`**，
//! 供 RefreshToken 等场景复用用户身份。因此本实现**关闭库自带的 exp 校验**，
//! 改为解析后自行判定：既能拿到 claims，又能区分「过期」与「非法」。
//!
//! ## 声明顺序
//!
//! JSON 字段顺序为 `user_id,email,role,token_version,sid,bnd,exp,iat,nbf`，
//! 与 Go 的结构体嵌入顺序一致。便于与 Go 版逐字节对照调试。

use serde::{Deserialize, Serialize};

/// 与 Go 版 `maxTokenLength` 一致。
pub const MAX_TOKEN_LENGTH: usize = 8192;

/// JWT 声明，字段与 Go 版 `JWTClaims` 对齐。
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct JwtClaims {
    pub user_id: i64,
    pub email: String,
    pub role: String,
    /// 用于在改密后使既有 token 失效。
    pub token_version: i64,
    /// 会话 ID（与 refresh token family 对应），用于单会话撤销与 step-up 绑定。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub sid: Option<String>,
    /// 会话指纹哈希（IP+UA），会话绑定开启时校验；空表示旧 token（平滑升级）。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub bnd: Option<String>,
    /// 过期时间（Unix 秒），对应 Go 的 `RegisteredClaims.ExpiresAt`。
    pub exp: i64,
    /// 签发时间（Unix 秒）。
    pub iat: i64,
    /// 生效时间（Unix 秒）。
    pub nbf: i64,
}

/// 校验失败的原因，对应 Go 版的 `ErrTokenExpired` / `ErrInvalidToken` / `ErrTokenTooLarge`。
#[derive(Debug)]
pub enum TokenError {
    /// 超出长度上限。
    TooLarge,
    /// token 非法（签名错误、算法不允许、格式错误等）。
    Invalid,
    /// token 已过期；携带解析出的 claims（与 Go 行为一致）。
    Expired(Box<JwtClaims>),
}

impl std::fmt::Display for TokenError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            TokenError::TooLarge => write!(f, "token too large"),
            TokenError::Invalid => write!(f, "invalid token"),
            TokenError::Expired(_) => write!(f, "token expired"),
        }
    }
}

impl std::error::Error for TokenError {}

/// 生成 16 进制随机字符串（`byte_length` 字节）。
///
/// 与 Go 版 `randomHexString` 一致：`SessionID` 使用 `random_hex(8)`，即 16 个字符。
pub fn random_hex(byte_length: usize) -> Option<String> {
    let n = if byte_length == 0 { 16 } else { byte_length };
    let mut buf = vec![0u8; n];
    getrandom::getrandom(&mut buf).ok()?;
    Some(buf.iter().map(|b| format!("{b:02x}")).collect())
}

/// 当前 Unix 秒。
fn now_unix() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

/// 签发有效期计算，对齐 Go 版 `generateAccessToken`：
/// 优先使用 `access_token_expire_minutes`，为 0 时回退到旧的 `expire_hour`。
pub fn access_token_expiry(now: i64, expire_minutes: i64, expire_hour: i64) -> i64 {
    if expire_minutes > 0 {
        now + expire_minutes * 60
    } else {
        now + expire_hour * 3600
    }
}

/// 签发 access token，对齐 Go 版 `generateAccessToken`。
///
/// `session_id` 由调用方生成（Go 版在 `GenerateToken` 中用 `random_hex(8)`）。
#[allow(clippy::too_many_arguments)]
pub fn encode_access_token(
    secret: &str,
    user_id: i64,
    email: &str,
    role: &str,
    token_version: i64,
    session_id: &str,
    binding_hash: &str,
    expire_minutes: i64,
    expire_hour: i64,
) -> Result<String, TokenError> {
    let now = now_unix();
    let claims = JwtClaims {
        user_id,
        email: email.to_string(),
        role: role.to_string(),
        token_version,
        sid: Some(session_id.to_string()),
        bnd: Some(binding_hash.to_string()),
        exp: access_token_expiry(now, expire_minutes, expire_hour),
        iat: now,
        nbf: now,
    };

    jsonwebtoken::encode(
        &jsonwebtoken::Header::new(jsonwebtoken::Algorithm::HS256),
        &claims,
        &jsonwebtoken::EncodingKey::from_secret(secret.as_bytes()),
    )
    .map_err(|_| TokenError::Invalid)
}

/// 校验 token，对齐 Go 版 `ValidateToken`。
///
/// 过期时返回 `Err(TokenError::Expired(claims))`，claims 仍可读取。
pub fn validate_token(secret: &str, token: &str) -> Result<JwtClaims, TokenError> {
    // 先做长度校验，尽早拒绝异常超长 token。
    if token.len() > MAX_TOKEN_LENGTH {
        return Err(TokenError::TooLarge);
    }

    let mut validation = jsonwebtoken::Validation::new(jsonwebtoken::Algorithm::HS256);
    // 允许的算法集合与 Go 版 WithValidMethods 一致，防算法混淆。
    validation.algorithms = vec![
        jsonwebtoken::Algorithm::HS256,
        jsonwebtoken::Algorithm::HS384,
        jsonwebtoken::Algorithm::HS512,
    ];
    // 关闭库自带的 exp 校验，改为解析后自行判定：
    // 这样过期时仍能拿到 claims（与 Go 行为一致）。
    validation.validate_exp = false;
    // nbf 仍由库校验：未生效的 token 视为非法（与 Go 一致）。
    validation.validate_nbf = true;
    validation.required_spec_claims.clear();

    let data = jsonwebtoken::decode::<JwtClaims>(
        token,
        &jsonwebtoken::DecodingKey::from_secret(secret.as_bytes()),
        &validation,
    )
    .map_err(|_| TokenError::Invalid)?;

    let claims = data.claims;
    if now_unix() >= claims.exp {
        return Err(TokenError::Expired(Box::new(claims)));
    }

    Ok(claims)
}

#[cfg(test)]
mod tests {
    use super::*;
    use base64::Engine as _;

    const SECRET: &str = "test-secret-key";

    #[test]
    fn roundtrip_preserves_claims() {
        let token = encode_access_token(
            SECRET,
            42,
            "a@b.com",
            "admin",
            3,
            "aabbccdd00112233",
            "deadbeef",
            60,
            24,
        )
        .unwrap();

        let claims = validate_token(SECRET, &token).unwrap();
        assert_eq!(claims.user_id, 42);
        assert_eq!(claims.email, "a@b.com");
        assert_eq!(claims.role, "admin");
        assert_eq!(claims.token_version, 3);
        assert_eq!(claims.sid.as_deref(), Some("aabbccdd00112233"));
        assert_eq!(claims.bnd.as_deref(), Some("deadbeef"));
        assert!(claims.exp > claims.iat);
        assert_eq!(claims.exp - claims.iat, 60 * 60);
    }

    #[test]
    fn wrong_secret_is_invalid() {
        let token = encode_access_token(SECRET, 1, "a@b.com", "user", 0, "s", "b", 60, 24).unwrap();
        assert!(matches!(
            validate_token("other-secret", &token),
            Err(TokenError::Invalid)
        ));
    }

    #[test]
    fn expired_token_returns_claims() {
        // expire_minutes = -1 → exp 落在过去。
        let token = encode_access_token(SECRET, 7, "a@b.com", "user", 0, "s", "b", -1, 0).unwrap();
        match validate_token(SECRET, &token) {
            Err(TokenError::Expired(claims)) => {
                assert_eq!(claims.user_id, 7, "过期时仍应返回 claims");
            }
            other => panic!("应返回 Expired 且携带 claims，实际: {other:?}"),
        }
    }

    #[test]
    fn overlong_token_rejected_before_parsing() {
        let huge = "a".repeat(MAX_TOKEN_LENGTH + 1);
        assert!(matches!(
            validate_token(SECRET, &huge),
            Err(TokenError::TooLarge)
        ));
    }

    #[test]
    fn garbage_is_invalid() {
        assert!(matches!(
            validate_token(SECRET, "not-a-jwt"),
            Err(TokenError::Invalid)
        ));
    }

    #[test]
    fn expiry_falls_back_to_hours_when_minutes_zero() {
        // 与 Go 版「minutes 为 0 时使用 expire_hour」一致。
        assert_eq!(access_token_expiry(1000, 0, 24), 1000 + 24 * 3600);
        assert_eq!(access_token_expiry(1000, 30, 24), 1000 + 30 * 60);
    }

    #[test]
    fn random_hex_has_expected_length() {
        let s = random_hex(8).unwrap();
        assert_eq!(s.len(), 16, "8 字节应为 16 个十六进制字符");
        assert!(s.chars().all(|c| c.is_ascii_hexdigit()));
    }

    #[test]
    fn random_hex_zero_defaults_to_16_bytes() {
        // 与 Go 版 `if byteLength <= 0 { byteLength = 16 }` 一致。
        assert_eq!(random_hex(0).unwrap().len(), 32);
    }

    #[test]
    fn random_hex_is_unique() {
        assert_ne!(random_hex(8).unwrap(), random_hex(8).unwrap());
    }

    /// 头部必须使用 HS256（与 Go 版 SigningMethodHS256 一致）。
    #[test]
    fn header_algorithm_is_hs256() {
        let token = encode_access_token(SECRET, 1, "a@b.com", "user", 0, "s", "b", 60, 24).unwrap();
        let header = jsonwebtoken::decode_header(&token).unwrap();
        assert_eq!(header.alg, jsonwebtoken::Algorithm::HS256);
    }

    /// **交叉兼容验证**：验证一个由 **Go 版实际签发**的 HS256 token。
    ///
    /// 该 token 由 Go 的 `crypto/hmac` + `crypto/sha256` 手工构造，claim 顺序与
    /// Go 结构体一致，secret 为 `unit-test-secret`。Rust 必须能验签通过并
    /// 解析出全部 claim —— 这是「Go 与 Rust 可互认 token」的直接证据。
    #[test]
    fn validates_token_signed_by_go() {
        const GO_SECRET: &str = "unit-test-secret";
        const GO_TOKEN: &str = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.\
eyJ1c2VyX2lkIjo0MiwiZW1haWwiOiJhQGIuY29tIiwicm9sZSI6ImFkbWluIiwidG9rZW5fdmVyc2lvbiI6Mywi\
c2lkIjoiYWFiYmNjZGQwMDExMjIzMyIsImJuZCI6ImRlYWRiZWVmIiwiZXhwIjo0MDAwMDAwMDAwLCJpYXQiOjEw\
MDAwMDAwMDAsIm5iZiI6MTAwMDAwMDAwMH0.w7s_vS7lX2r17qyBejVmT77G_33ouokZ7aUUk-WOSNI";

        let claims = validate_token(GO_SECRET, GO_TOKEN).expect("Go 签发的 token 应通过校验");
        assert_eq!(claims.user_id, 42);
        assert_eq!(claims.email, "a@b.com");
        assert_eq!(claims.role, "admin");
        assert_eq!(claims.token_version, 3);
        assert_eq!(claims.sid.as_deref(), Some("aabbccdd00112233"));
        assert_eq!(claims.bnd.as_deref(), Some("deadbeef"));
        assert_eq!(claims.exp, 4_000_000_000);
        assert_eq!(claims.iat, 1_000_000_000);
        assert_eq!(claims.nbf, 1_000_000_000);
    }

    /// 同一 token 用错误密钥必须被拒绝（确认上一条不是因为校验被跳过）。
    #[test]
    fn go_token_rejected_with_wrong_secret() {
        const GO_TOKEN: &str = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.\
eyJ1c2VyX2lkIjo0MiwiZW1haWwiOiJhQGIuY29tIiwicm9sZSI6ImFkbWluIiwidG9rZW5fdmVyc2lvbiI6Mywi\
c2lkIjoiYWFiYmNjZGQwMDExMjIzMyIsImJuZCI6ImRlYWRiZWVmIiwiZXhwIjo0MDAwMDAwMDAwLCJpYXQiOjEw\
MDAwMDAwMDAsIm5iZiI6MTAwMDAwMDAwMH0.w7s_vS7lX2r17qyBejVmT77G_33ouokZ7aUUk-WOSNI";
        assert!(matches!(
            validate_token("wrong-secret", GO_TOKEN),
            Err(TokenError::Invalid)
        ));
    }

    /// **反向兼容验证**：Rust 签发的 token，去掉签名后其 payload 可被独立解码，
    /// 且 claim 名称与 Go 结构体 json tag 一致。
    #[test]
    fn rust_token_claims_use_go_field_names() {
        let token =
            encode_access_token(SECRET, 9, "z@y.com", "user", 2, "sid123", "bnd123", 60, 24)
                .unwrap();

        // 拆出 payload 段做 base64url 解码，核对字段名。
        let payload_b64 = token.split('.').nth(1).expect("payload 段");
        let payload = base64::engine::general_purpose::URL_SAFE_NO_PAD
            .decode(payload_b64)
            .expect("payload 应为 base64url");
        let v: serde_json::Value = serde_json::from_slice(&payload).unwrap();

        for key in [
            "user_id",
            "email",
            "role",
            "token_version",
            "sid",
            "bnd",
            "exp",
            "iat",
            "nbf",
        ] {
            assert!(v.get(key).is_some(), "缺少 claim: {key}");
        }
        assert_eq!(v["user_id"], 9);
        assert_eq!(v["sid"], "sid123");
    }
}
