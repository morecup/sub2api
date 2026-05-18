-- Add proxy traffic accounting fields to usage logs.
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS proxy_id BIGINT NULL,
    ADD COLUMN IF NOT EXISTS request_traffic_bytes BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS response_traffic_bytes BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS total_traffic_bytes BIGINT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_usage_logs_proxy_id ON usage_logs (proxy_id);
CREATE INDEX IF NOT EXISTS idx_usage_logs_proxy_id_created_at ON usage_logs (proxy_id, created_at);
