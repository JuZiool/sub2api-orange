-- 9002: Orange 特有 —— 账号多代理池绑定表（账号多代理轮换）
-- 单代理账号继续使用 accounts.proxy_id，不写池表；仅当选择 >=2 个代理时写入。
CREATE TABLE IF NOT EXISTS account_proxies (
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    proxy_id BIGINT NOT NULL REFERENCES proxies(id) ON DELETE CASCADE,
    position INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (account_id, proxy_id)
);
CREATE INDEX IF NOT EXISTS idx_account_proxies_proxy_id ON account_proxies(proxy_id);
CREATE INDEX IF NOT EXISTS idx_account_proxies_account_position ON account_proxies(account_id, position);
