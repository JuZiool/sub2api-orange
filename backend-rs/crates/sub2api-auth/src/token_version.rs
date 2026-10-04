//! TokenVersion 指纹派生。
//!
//! ## 为什么需要这个
//!
//! `users` 表**没有 `token_version` 列**（见 Go 版 `internal/service/auth_service.go`
//! 的注释）。JWT 里的 `token_version` 是由 `email + password_hash` **派生**的指纹：
//!
//! - 密码变更 → `password_hash` 变化 → 指纹变化 → 旧 token 全部失效；
//! - 无需为撤销单独维护数据库字段。
//!
//! 因此改密撤销 token 的能力**完全依赖这个算法逐位一致**。若 Rust 侧算出不同
//! 的值，会导致：Go 签发的 token 被 Rust 拒绝（或反之），用户被误登出。
//!
//! ## 算法（与 Go 版 `resolvedTokenVersion` 逐字对齐）
//!
//! ```text
//! material    = lowercase(trim(email)) + "\n" + password_hash
//! digest      = SHA256(material)
//! fingerprint = int64(big_endian_u64(digest[0..8]) & 0x7fff_ffff_ffff_ffff)
//! result      = token_version XOR fingerprint
//! ```
//!
//! 两个易错点：
//! 1. 掩码只清除**最高位**（`0x7fff...`，63 位），不是 `0xffff...`；
//! 2. XOR 的右操作数是**掩码后的**指纹，不是原始 u64。

use sha2::{Digest, Sha256};

/// 派生 TokenVersion 指纹，对齐 Go 版 `resolvedTokenVersion`。
///
/// `token_version_resolved` 为真时直接返回 `token_version`（对应 Go 的
/// `TokenVersionResolved` 短路分支）。
pub fn resolved_token_version(
    email: &str,
    password_hash: &str,
    token_version: i64,
    token_version_resolved: bool,
) -> i64 {
    if token_version_resolved {
        return token_version;
    }

    let material = format!("{}\n{}", email.trim().to_lowercase(), password_hash);
    let digest = Sha256::digest(material.as_bytes());

    // 取前 8 字节按大端解析为 u64，再清除最高位。
    let mut buf = [0u8; 8];
    buf.copy_from_slice(&digest[..8]);
    let raw = u64::from_be_bytes(buf);
    let fingerprint = (raw & 0x7fff_ffff_ffff_ffff) as i64;

    token_version ^ fingerprint
}

#[cfg(test)]
mod tests {
    use super::*;

    /// **对照向量由 Go 版 `resolvedTokenVersion` 实际运行生成**，
    /// 用于锁定 Go↔Rust 位级一致。生成方式见提交说明。
    #[test]
    fn matches_go_reference_vectors() {
        let cases: [(&str, &str, i64, bool, i64); 5] = [
            (
                "Admin@Example.COM",
                "$2a$10$abcdefghijklmnopqrstuv",
                0,
                false,
                7186968893809747875,
            ),
            ("user@test.com", "hash123", 5, false, 3437823664235456858),
            ("", "", 0, false, 124490115762057193),
            ("  Spaced@X.com  ", "h", 7, false, 3593717462336601017),
            // resolved=true → 短路，直接返回 token_version。
            ("a@b.com", "p", 1, true, 1),
        ];

        for (email, hash, tv, resolved, expected) in cases {
            let got = resolved_token_version(email, hash, tv, resolved);
            assert_eq!(
                got, expected,
                "email={email:?} hash={hash:?} tv={tv} resolved={resolved}"
            );
        }
    }

    /// email 大小写与首尾空白不影响结果（与 Go 的 ToLower+TrimSpace 一致）。
    #[test]
    fn email_is_normalized() {
        let a = resolved_token_version("USER@TEST.COM", "hash123", 5, false);
        let b = resolved_token_version("user@test.com", "hash123", 5, false);
        let c = resolved_token_version("  user@test.com  ", "hash123", 5, false);
        assert_eq!(a, b);
        assert_eq!(b, c);
    }

    /// password_hash 大小写**敏感**（与 Go 一致，未做 lower）。
    #[test]
    fn password_hash_is_case_sensitive() {
        let a = resolved_token_version("u@t.com", "Hash", 0, false);
        let b = resolved_token_version("u@t.com", "hash", 0, false);
        assert_ne!(a, b);
    }

    /// 结果恒非负（最高位被掩码清除）。
    #[test]
    fn result_is_non_negative() {
        for email in ["a@b.com", "", "x", "long@example.com"] {
            let v = resolved_token_version(email, "hash", 0, false);
            assert!(v >= 0, "指纹应非负，实际 {v}");
        }
    }

    /// 改密（password_hash 变化）必须改变指纹——这是撤销机制的基础。
    #[test]
    fn password_change_changes_fingerprint() {
        let before = resolved_token_version("a@b.com", "old-hash", 0, false);
        let after = resolved_token_version("a@b.com", "new-hash", 0, false);
        assert_ne!(before, after, "改密后指纹必须变化");
    }
}
