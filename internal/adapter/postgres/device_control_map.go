package postgres

import (
	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/devicecontrol"
)

func toCommand(row sqlcgen.DeviceControlCommand) devicecontrol.Command {
	cmd := devicecontrol.Command{
		ID: uuidString(row.ID), DeviceID: uuidString(row.DeviceID),
		Type: row.CommandType, Version: int(row.CommandVersion),
		IdempotencyKey: row.IdempotencyKey, Status: row.Status,
		RequestedAt:     row.RequestedAt.Time.UTC(),
		LeaseGeneration: row.LeaseGeneration,
	}
	if row.LeasedAt.Valid {
		t := row.LeasedAt.Time.UTC()
		cmd.LeasedAt = &t
	}
	if row.LeaseUntil.Valid {
		t := row.LeaseUntil.Time.UTC()
		cmd.LeaseUntil = &t
	}
	if row.AcceptedAt.Valid {
		t := row.AcceptedAt.Time.UTC()
		cmd.AcceptedAt = &t
	}
	if row.RunningAt.Valid {
		t := row.RunningAt.Time.UTC()
		cmd.RunningAt = &t
	}
	if row.FinishedAt.Valid {
		t := row.FinishedAt.Time.UTC()
		cmd.FinishedAt = &t
	}
	if row.ResultCode.Valid {
		s := row.ResultCode.String
		cmd.ResultCode = &s
	}
	return cmd
}

func toPresence(row sqlcgen.DeviceControlPresence) devicecontrol.Presence {
	p := devicecontrol.Presence{DeviceID: uuidString(row.DeviceID)}
	if row.LastSeenAt.Valid {
		t := row.LastSeenAt.Time.UTC()
		p.LastSeenAt = &t
	}
	if row.LastPollAt.Valid {
		t := row.LastPollAt.Time.UTC()
		p.LastPollAt = &t
	}
	if row.LastCommandAcceptedAt.Valid {
		t := row.LastCommandAcceptedAt.Time.UTC()
		p.LastAcceptedAt = &t
	}
	if row.LastCommandFinishedAt.Valid {
		t := row.LastCommandFinishedAt.Time.UTC()
		p.LastFinishedAt = &t
	}
	return p
}
