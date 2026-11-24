-- Orange 特有：请求延迟拆分（网关准备耗时 / 等待上游耗时）。
-- 只新建自己的表；usage_logs 等上游表只读，不新增列。
-- 口径：
--   gateway_ms  = 从网关收到请求到把上游请求发出去（鉴权、调度选号、协议转换、建连前准备）
--   upstream_ms = 从发出上游请求到收到首个内容块（含到上游的 RTT、上游排队与推理时间）
-- 两者之和 ≈ usage_logs.first_token_ms（流式请求）；非流式或未采样时无记录。

CREATE TABLE IF NOT EXISTS orange_usage_latency (
    request_id  VARCHAR(128) PRIMARY KEY,
    gateway_ms  INT NOT NULL CHECK (gateway_ms >= 0),
    upstream_ms INT NOT NULL CHECK (upstream_ms >= 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE  orange_usage_latency IS
    'Orange 特有：首字延迟拆分（gateway_ms 网关准备 + upstream_ms 等待上游到首字）';
COMMENT ON COLUMN orange_usage_latency.gateway_ms IS
    '从网关收到请求到上游请求发出（鉴权/调度/协议转换等本地耗时）';
COMMENT ON COLUMN orange_usage_latency.upstream_ms IS
    '从上游请求发出到首个内容块（网络 RTT + 上游排队与推理）';

CREATE INDEX IF NOT EXISTS idx_orange_usage_latency_created_at
    ON orange_usage_latency(created_at);
