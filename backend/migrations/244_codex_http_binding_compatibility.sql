-- This preset only executes standard OpenAI OAuth. Apply the same invariant to
-- every persistence path, including legacy snapshot writers and raw SQL token
-- updates. Other drivers retain their existing platform/authentication rules.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'accounts_codex_http_binding_compatible'
          AND conrelid = 'accounts'::regclass
    ) THEN
        ALTER TABLE accounts ADD CONSTRAINT accounts_codex_http_binding_compatible
        CHECK (
            account_driver_id <> 'codex-rs-http'
            OR (
                platform = 'openai' AND type = 'oauth' AND parent_account_id IS NULL
                AND COALESCE(jsonb_typeof(credentials -> 'auth_mode'), 'null') IN ('null', 'string')
                AND COALESCE(lower(btrim(credentials ->> 'auth_mode')), '') IN ('', 'oauth')
                AND COALESCE(jsonb_typeof(credentials -> 'openai_auth_mode'), 'null') IN ('null', 'string')
                AND COALESCE(lower(btrim(credentials ->> 'openai_auth_mode')), '') IN ('', 'oauth')
                AND COALESCE(jsonb_typeof(extra -> 'auth_mode'), 'null') IN ('null', 'string')
                AND COALESCE(lower(btrim(extra ->> 'auth_mode')), '') IN ('', 'oauth')
                AND COALESCE(jsonb_typeof(extra -> 'openai_auth_mode'), 'null') IN ('null', 'string')
                AND COALESCE(lower(btrim(extra ->> 'openai_auth_mode')), '') IN ('', 'oauth')
            )
        );
    END IF;
END $$;
