-- 每个业务分组、平台只有一个 CPR 池入口，真实账号始终保存在 CPR。
CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_cpr_business_pool
ON accounts ((extra->'cpr_pool'->>'group_id'), platform)
WHERE account_driver_id = 'cpr-managed' AND type = 'apikey' AND deleted_at IS NULL;
