-- +goose Up
-- Keep removed configurations and every historical send. Existing UUIDs,
-- recipients, schedules, report bodies, and notification rows are retained.
ALTER TABLE notification_recipients ADD COLUMN deleted_at TIMESTAMPTZ;
ALTER TABLE business_report_schedules ADD COLUMN deleted_at TIMESTAMPTZ;
ALTER TABLE business_report_schedules DROP CONSTRAINT business_report_schedules_name_key;
CREATE UNIQUE INDEX idx_report_schedule_active_name ON business_report_schedules (name) WHERE deleted_at IS NULL;

-- Runs must keep their original kind/name when the schedule is edited.
ALTER TABLE business_report_runs ADD COLUMN report_kind TEXT;
ALTER TABLE business_report_runs ADD COLUMN schedule_name TEXT;
UPDATE business_report_runs r SET report_kind=s.report_kind, schedule_name=s.name
FROM business_report_schedules s WHERE s.id=r.schedule_id;
ALTER TABLE business_report_runs ALTER COLUMN report_kind SET NOT NULL;
ALTER TABLE business_report_runs ALTER COLUMN schedule_name SET NOT NULL;
ALTER TABLE business_report_runs ADD CONSTRAINT report_run_kind_valid CHECK (report_kind IN ('daily','ten_day'));

-- The trigger also supports the previous writer during an upgrade: old
-- inserts omit these fields but still get an immutable snapshot.
-- +goose StatementBegin
CREATE FUNCTION snapshot_business_report_run() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  SELECT s.report_kind, s.name INTO NEW.report_kind, NEW.schedule_name
  FROM business_report_schedules s WHERE s.id=NEW.schedule_id;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER snapshot_business_report_run BEFORE INSERT ON business_report_runs
FOR EACH ROW EXECUTE FUNCTION snapshot_business_report_run();

-- +goose Down
-- Refuse a downgrade that could revive removed configurations or rewrite
-- the meaning of existing report history. Restore a backup if required.
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM notification_recipients WHERE deleted_at IS NOT NULL)
     OR EXISTS (SELECT 1 FROM business_report_schedules WHERE deleted_at IS NOT NULL)
     OR EXISTS (SELECT 1 FROM business_report_runs r JOIN business_report_schedules s ON s.id=r.schedule_id
                WHERE r.report_kind<>s.report_kind OR r.schedule_name<>s.name) THEN
    RAISE EXCEPTION 'report configuration downgrade would discard retained configuration or history';
  END IF;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER snapshot_business_report_run ON business_report_runs;
DROP FUNCTION snapshot_business_report_run();
ALTER TABLE business_report_runs DROP CONSTRAINT report_run_kind_valid;
ALTER TABLE business_report_runs DROP COLUMN schedule_name, DROP COLUMN report_kind;
DROP INDEX idx_report_schedule_active_name;
ALTER TABLE business_report_schedules ADD CONSTRAINT business_report_schedules_name_key UNIQUE (name);
ALTER TABLE business_report_schedules DROP COLUMN deleted_at;
ALTER TABLE notification_recipients DROP COLUMN deleted_at;
