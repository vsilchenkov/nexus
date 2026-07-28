-- 0030_instance_ch_claimed.down.sql
ALTER TABLE instance_identity DROP COLUMN IF EXISTS ch_claimed;
