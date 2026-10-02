-- +goose Up
-- Telegram permits 32 username characters plus the @ destination prefix.
-- Existing TEXT values and immutable delivery snapshots are never rewritten.
ALTER TABLE notification_messages DROP CONSTRAINT notification_messages_recipient_check;
ALTER TABLE notification_messages ADD CONSTRAINT notification_messages_recipient_check CHECK (char_length(recipient) BETWEEN 1 AND 33);
ALTER TABLE business_report_recipients DROP CONSTRAINT business_report_recipients_recipient_check;
ALTER TABLE business_report_recipients ADD CONSTRAINT business_report_recipients_recipient_check CHECK (char_length(recipient) BETWEEN 1 AND 33);
ALTER TABLE operational_alert_recipients DROP CONSTRAINT operational_alert_recipients_recipient_check;
ALTER TABLE operational_alert_recipients ADD CONSTRAINT operational_alert_recipients_recipient_check CHECK (char_length(recipient) BETWEEN 1 AND 33);

-- +goose Down
-- Reinstating the old bound fails transactionally if any wider value remains.
-- Resolve those records explicitly or restore a compatible backup; never truncate.
ALTER TABLE notification_messages DROP CONSTRAINT notification_messages_recipient_check;
ALTER TABLE notification_messages ADD CONSTRAINT notification_messages_recipient_check CHECK (char_length(recipient) BETWEEN 1 AND 32);
ALTER TABLE business_report_recipients DROP CONSTRAINT business_report_recipients_recipient_check;
ALTER TABLE business_report_recipients ADD CONSTRAINT business_report_recipients_recipient_check CHECK (char_length(recipient) BETWEEN 1 AND 32);
ALTER TABLE operational_alert_recipients DROP CONSTRAINT operational_alert_recipients_recipient_check;
ALTER TABLE operational_alert_recipients ADD CONSTRAINT operational_alert_recipients_recipient_check CHECK (char_length(recipient) BETWEEN 1 AND 32);
