ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS weight INT NOT NULL DEFAULT 1;

ALTER TABLE accounts
    DROP CONSTRAINT IF EXISTS accounts_weight_positive;

ALTER TABLE accounts
    ADD CONSTRAINT accounts_weight_positive CHECK (weight BETWEEN 1 AND 10000);
