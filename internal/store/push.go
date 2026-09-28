package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Alert kinds. The kind decides the delivery channels (see alertChannels).
const (
	AlertProjectDown   = "project_down"
	AlertRunnerOffline = "runner_offline"
	AlertCIFailed      = "ci_failed"
	AlertAgentWaiting  = "agent_waiting"
)

// MaxPushSubscriptions bounds the devices one operator can register.
const MaxPushSubscriptions = 20

var ErrTooManySubscriptions = errors.New("too many push subscriptions")

// alertChannels routes by severity: production incidents go to e-mail and
// Web Push; an agent waiting for input is only worth a push notification.
func alertChannels(kind string) []string {
	if kind == AlertAgentWaiting {
		return []string{"webpush"}
	}
	return []string{"smtp", "webpush"}
}

// alertRepeatWindow suppresses a repeated alert for the same subject.
// Agent sessions notify on each transition into "waiting", so their window
// only absorbs flapping between polls.
func alertRepeatWindow(kind string) time.Duration {
	if kind == AlertAgentWaiting {
		return 5 * time.Minute
	}
	return time.Hour
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// QueueAlert records an event for every channel its kind routes to. SMTP
// rows wait with sent_at=0 until the mailer delivers them. A webpush row is
// written only when a device is subscribed; it fans out into one delivery
// per subscription and is marked sent once every delivery has finished.
func (s *Store) QueueAlert(ctx context.Context, kind, subject, body string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := queueAlert(ctx, tx, nil, kind, subject, body, time.Now()); err != nil {
		return err
	}
	return tx.Commit()
}

// QueueProjectAlert records the alert against its project and the check time
// that crossed the threshold, so the incident history can place it later.
func (s *Store) QueueProjectAlert(ctx context.Context, projectID int64, kind, subject, body string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := queueAlert(ctx, tx, projectID, kind, subject, body, at); err != nil {
		return err
	}
	return tx.Commit()
}

func queueAlert(ctx context.Context, tx execer, projectID any, kind, subject, body string, now time.Time) error {
	cutoff := now.Add(-alertRepeatWindow(kind)).Unix()
	for _, channel := range alertChannels(kind) {
		condition := ""
		if channel == "webpush" {
			condition = " AND EXISTS (SELECT 1 FROM push_subscriptions)"
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO alerts(kind,subject,body,sent_at,channel,project_id,created_at)
SELECT ?,?,?,0,?,?,? WHERE NOT EXISTS (SELECT 1 FROM alerts WHERE kind=? AND subject=? AND channel=? AND (sent_at=0 OR sent_at>?))`+condition,
			kind, subject, body, channel, projectID, now.Unix(), kind, subject, channel, cutoff)
		if err != nil {
			return err
		}
		if channel != "webpush" {
			continue
		}
		if n, _ := result.RowsAffected(); n == 0 {
			continue
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO push_deliveries(subscription_id,alert_id,attempts,next_attempt_at,created_at)
SELECT id,?,0,?,? FROM push_subscriptions`, id, now.Unix(), now.Unix()); err != nil {
			return err
		}
	}
	return nil
}

type PushSubscription struct {
	ID       int64
	Endpoint string
	P256DH   string // base64url uncompressed P-256 point from the browser
	Auth     string // base64url 16-byte authentication secret
}

type PushDelivery struct {
	ID           int64
	Subscription PushSubscription
	AlertID      int64
	Kind         string
	Subject      string
	Body         string
	Attempts     int
	CreatedAt    int64
}

// SavePushSubscription registers a device, or refreshes its keys when the
// endpoint is already known.
func (s *Store) SavePushSubscription(ctx context.Context, sub PushSubscription, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists, count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM push_subscriptions WHERE endpoint=?`, sub.Endpoint).Scan(&exists); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM push_subscriptions`).Scan(&count); err != nil {
		return err
	}
	if exists == 0 && count >= MaxPushSubscriptions {
		return ErrTooManySubscriptions
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO push_subscriptions(endpoint,p256dh,auth,created_at) VALUES (?,?,?,?)
ON CONFLICT(endpoint) DO UPDATE SET p256dh=excluded.p256dh,auth=excluded.auth`, sub.Endpoint, sub.P256DH, sub.Auth, at.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// PushSubscriptionByEndpoint returns sql.ErrNoRows for an unknown endpoint.
func (s *Store) PushSubscriptionByEndpoint(ctx context.Context, endpoint string) (PushSubscription, error) {
	var sub PushSubscription
	err := s.db.QueryRowContext(ctx, `SELECT id,endpoint,p256dh,auth FROM push_subscriptions WHERE endpoint=?`, endpoint).Scan(&sub.ID, &sub.Endpoint, &sub.P256DH, &sub.Auth)
	return sub, err
}

func (s *Store) CountPushSubscriptions(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM push_subscriptions`).Scan(&count)
	return count, err
}

// DeletePushSubscription removes a device and its pending deliveries.
func (s *Store) DeletePushSubscription(ctx context.Context, id int64, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM push_deliveries WHERE subscription_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE id=?`, id); err != nil {
		return err
	}
	if err := finishPushAlerts(ctx, tx, at); err != nil {
		return err
	}
	return tx.Commit()
}

// DuePushDeliveries lists queued deliveries whose next attempt is due.
func (s *Store) DuePushDeliveries(ctx context.Context, now time.Time, limit int) ([]PushDelivery, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id,d.attempts,d.created_at,s.id,s.endpoint,s.p256dh,s.auth,a.id,a.kind,a.subject,a.body
FROM push_deliveries d JOIN push_subscriptions s ON s.id=d.subscription_id JOIN alerts a ON a.id=d.alert_id
WHERE d.next_attempt_at<=? ORDER BY d.next_attempt_at,d.id LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PushDelivery{}
	for rows.Next() {
		var d PushDelivery
		if err := rows.Scan(&d.ID, &d.Attempts, &d.CreatedAt, &d.Subscription.ID, &d.Subscription.Endpoint, &d.Subscription.P256DH, &d.Subscription.Auth, &d.AlertID, &d.Kind, &d.Subject, &d.Body); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

// FinishPushDelivery removes a delivery that succeeded or was abandoned and
// marks its alert sent when no other device is still waiting for it.
func (s *Store) FinishPushDelivery(ctx context.Context, id int64, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM push_deliveries WHERE id=?`, id); err != nil {
		return err
	}
	if err := finishPushAlerts(ctx, tx, at); err != nil {
		return err
	}
	return tx.Commit()
}

// RetryPushDelivery schedules another attempt.
func (s *Store) RetryPushDelivery(ctx context.Context, id int64, attempts int, next time.Time, lastError string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE push_deliveries SET attempts=?,next_attempt_at=?,last_error=? WHERE id=?`, attempts, next.Unix(), lastError, id)
	return err
}

func finishPushAlerts(ctx context.Context, tx execer, at time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE alerts SET sent_at=? WHERE channel='webpush' AND sent_at=0
AND NOT EXISTS (SELECT 1 FROM push_deliveries WHERE alert_id=alerts.id)`, at.Unix())
	return err
}
