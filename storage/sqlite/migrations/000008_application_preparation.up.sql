ALTER TABLE applications ADD COLUMN decision_code TEXT NOT NULL DEFAULT '';
ALTER TABLE applications ADD COLUMN decision_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE applications ADD COLUMN prepared_message TEXT NOT NULL DEFAULT '';
ALTER TABLE applications ADD COLUMN prepared_at INTEGER;
