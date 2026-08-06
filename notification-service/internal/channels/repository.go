package channels

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned when a channel id doesn't exist for the tenant.
var ErrNotFound = errors.New("channel not found")

// Repository persists notification channels, encrypting secret config values at
// rest. Safe for concurrent use (delegates to *sql.DB).
type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

// encryptConfig returns a copy of cfg with secret keys encrypted.
func encryptConfig(channelType string, cfg map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(cfg))
	for k, v := range cfg {
		if IsSecret(channelType, k) && v != "" {
			enc, err := Encrypt(v)
			if err != nil {
				return nil, err
			}
			out[k] = enc
			continue
		}
		out[k] = v
	}
	return out, nil
}

// decryptConfig returns a copy of cfg with secret keys decrypted (for delivery).
func decryptConfig(channelType string, cfg map[string]string) map[string]string {
	out := make(map[string]string, len(cfg))
	for k, v := range cfg {
		if IsSecret(channelType, k) && v != "" {
			if dec, err := Decrypt(v); err == nil {
				out[k] = dec
				continue
			}
			// A decrypt failure (e.g. key rotated) leaves the secret unusable;
			// deliver-path callers treat an empty secret as "not configured".
			out[k] = ""
			continue
		}
		out[k] = v
	}
	return out
}

// Create inserts a channel, encrypting its secrets. Requires an encryption key.
func (r *Repository) Create(ctx context.Context, ch *Channel) (*Channel, error) {
	if !EncryptionConfigured() {
		return nil, ErrNoEncryptionKey
	}
	enc, err := encryptConfig(ch.Type, ch.Config)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(enc)
	ch.ID = uuid.New().String()
	ch.CreatedAt = time.Now().UTC()
	ch.UpdatedAt = ch.CreatedAt
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO notification_channels (id, tenant_id, name, type, config, enabled, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		ch.ID, ch.TenantID, ch.Name, ch.Type, raw, ch.Enabled, ch.CreatedAt, ch.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return ch, nil
}

// Update modifies name/enabled/config. Secret values that arrive empty are kept
// from the stored row (the UI never receives secrets back, so it can't resend
// them) — only a non-empty secret replaces the existing one.
func (r *Repository) Update(ctx context.Context, tenantID, id string, name string, enabled bool, cfg map[string]string) (*Channel, error) {
	existing, err := r.getDecrypted(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	merged := map[string]string{}
	for k, v := range existing.Config {
		merged[k] = v
	}
	for k, v := range cfg {
		if IsSecret(existing.Type, k) && v == "" {
			continue // keep existing secret
		}
		merged[k] = v
	}
	enc, err := encryptConfig(existing.Type, merged)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(enc)
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx,
		`UPDATE notification_channels SET name=$1, enabled=$2, config=$3, updated_at=$4 WHERE tenant_id=$5 AND id=$6`,
		name, enabled, raw, now, tenantID, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	existing.Name = name
	existing.Enabled = enabled
	existing.Config = merged
	existing.UpdatedAt = now
	return existing, nil
}

func (r *Repository) Delete(ctx context.Context, tenantID, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM notification_channels WHERE tenant_id=$1 AND id=$2`, tenantID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListDecrypted returns a tenant's enabled channels with secrets decrypted, for
// the delivery path. On a missing table (migration not yet applied) it returns
// empty rather than erroring, so delivery degrades to the env/log channels.
func (r *Repository) ListDecrypted(ctx context.Context, tenantID string) ([]Channel, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, tenant_id, name, type, config, enabled, created_at, updated_at
		 FROM notification_channels WHERE tenant_id=$1 AND enabled=true ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChannels(rows, true)
}

// ListForAPI returns a tenant's channels with secrets redacted, for the UI.
func (r *Repository) ListForAPI(ctx context.Context, tenantID string) ([]Channel, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, tenant_id, name, type, config, enabled, created_at, updated_at
		 FROM notification_channels WHERE tenant_id=$1 ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chans, err := scanChannels(rows, true)
	if err != nil {
		return nil, err
	}
	out := make([]Channel, len(chans))
	for i, c := range chans {
		out[i] = c.Redacted()
	}
	return out, nil
}

// getDecrypted loads one channel with secrets decrypted (test-send / update).
func (r *Repository) getDecrypted(ctx context.Context, tenantID, id string) (*Channel, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, type, config, enabled, created_at, updated_at
		 FROM notification_channels WHERE tenant_id=$1 AND id=$2`, tenantID, id)
	var c Channel
	var raw []byte
	if err := row.Scan(&c.ID, &c.TenantID, &c.Name, &c.Type, &raw, &c.Enabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var stored map[string]string
	_ = json.Unmarshal(raw, &stored)
	c.Config = decryptConfig(c.Type, stored)
	return &c, nil
}

// Get returns one channel with secrets decrypted (for test-send).
func (r *Repository) Get(ctx context.Context, tenantID, id string) (*Channel, error) {
	return r.getDecrypted(ctx, tenantID, id)
}

func scanChannels(rows *sql.Rows, decrypt bool) ([]Channel, error) {
	out := []Channel{}
	for rows.Next() {
		var c Channel
		var raw []byte
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Name, &c.Type, &raw, &c.Enabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		var stored map[string]string
		_ = json.Unmarshal(raw, &stored)
		if decrypt {
			c.Config = decryptConfig(c.Type, stored)
		} else {
			c.Config = stored
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
