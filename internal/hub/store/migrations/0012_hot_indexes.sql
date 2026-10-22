-- Version 12: indexes for the alert engine's reconcile (alerts of one seat or machine) and the
-- push flood cap and target cleanup (deliveries of one target). Without them each ingest batch
-- and each delivery scanned the whole alert or delivery log, which only grows.

CREATE INDEX alert_seat ON alert(seat_id) WHERE seat_id IS NOT NULL;
CREATE INDEX alert_machine ON alert(machine_id) WHERE machine_id IS NOT NULL;
CREATE INDEX delivery_target ON delivery(target_id, status, updated_at);
