-- HSI IPv6 PPPoE session state reported by the node over Kafka. These columns
-- are nullable because older rows and nodes without IPv6 support have no value.
ALTER TABLE IF EXISTS pppoe_status ADD COLUMN IF NOT EXISTS hsi_ipv6 TEXT;
ALTER TABLE IF EXISTS pppoe_status ADD COLUMN IF NOT EXISTS hsi_ipv6_pd_prefix TEXT;
ALTER TABLE IF EXISTS pppoe_status ADD COLUMN IF NOT EXISTS hsi_ipv6_dns TEXT;
