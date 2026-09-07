// dataplane_sqlite.go — SQLiteRegistry implementations for the v0.5
// DataPlane entity (CP/DP architectural separation).
//
// Every Data Plane is a named inference cluster governed by this Control Plane.
// The join token is generated here, its SHA-256 hash stored; the plaintext
// is returned exactly once from CreateDataPlane and never persisted.
//
// Schema: dataplanes table defined in schema.sql; nodes.dataplane_id column
// added additively by ensureColumn in sqlite.go Migrate.
package registry

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

const dataplaneCols = `id, name, description, tier, gateway_url, status, join_token_hash, config_snapshot, last_heartbeat, created_at, updated_at`

func scanDataPlane(s interface{ Scan(...any) error }) (*DataPlane, error) {
	var (
		dp               DataPlane
		configSnap       sql.NullString
		lastHeartbeat    sql.NullString
		created, updated sql.NullString
	)
	if err := s.Scan(
		&dp.ID, &dp.Name, &dp.Description, &dp.Tier, &dp.GatewayURL,
		&dp.Status, &dp.JoinTokenHash,
		&configSnap, &lastHeartbeat, &created, &updated,
	); err != nil {
		return nil, err
	}
	if configSnap.Valid && configSnap.String != "" && configSnap.String != "{}" {
		var m map[string]any
		if err := json.Unmarshal([]byte(configSnap.String), &m); err == nil {
			dp.ConfigSnapshot = m
		}
	}
	if lastHeartbeat.Valid && lastHeartbeat.String != "" {
		t, err := time.Parse(tsLayout, lastHeartbeat.String)
		if err == nil {
			dp.LastHeartbeat = &t
		}
	}
	dp.CreatedAt = parseTime(created)
	dp.UpdatedAt = parseTime(updated)
	return &dp, nil
}

// generateJoinToken produces a random 32-byte token with a "dp_" prefix and
// returns both the plaintext (for the caller) and its SHA-256 hex hash.
func generateJoinToken() (plaintext, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", fmt.Errorf("registry: generate join token: %w", err)
	}
	plaintext = "dp_" + hex.EncodeToString(b)
	sum := sha256.Sum256([]byte(plaintext))
	hash = hex.EncodeToString(sum[:])
	return plaintext, hash, nil
}

// ─── CRUD ─────────────────────────────────────────────────────────────────────

// CreateDataPlane generates a join token, stores its hash, and returns the
// plaintext token exactly once. dp.ID, dp.JoinTokenHash, dp.CreatedAt and
// dp.UpdatedAt are written back into dp.
func (r *SQLiteRegistry) CreateDataPlane(ctx context.Context, dp *DataPlane) (joinToken string, err error) {
	now := nowUTC()
	if dp.ID == "" {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		dp.ID = "dp-" + hex.EncodeToString(b)
	}
	if dp.Tier == "" {
		dp.Tier = "production"
	}
	if dp.Status == "" {
		dp.Status = "registering"
	}
	dp.CreatedAt = now
	dp.UpdatedAt = now

	joinToken, dp.JoinTokenHash, err = generateJoinToken()
	if err != nil {
		return "", err
	}

	snapJSON := "{}"
	if dp.ConfigSnapshot != nil {
		b, _ := json.Marshal(dp.ConfigSnapshot)
		snapJSON = string(b)
	}

	_, dbErr := r.db.ExecContext(ctx, `
		INSERT INTO dataplanes
			(id, name, description, tier, gateway_url, status, join_token_hash, config_snapshot, last_heartbeat, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)`,
		dp.ID, dp.Name, dp.Description, dp.Tier, dp.GatewayURL,
		dp.Status, dp.JoinTokenHash, snapJSON,
		fmtTime(dp.CreatedAt), fmtTime(dp.UpdatedAt))
	if dbErr != nil {
		return "", fmt.Errorf("registry: create data plane %q: %w", dp.ID, dbErr)
	}
	return joinToken, nil
}

func (r *SQLiteRegistry) GetDataPlane(ctx context.Context, id string) (*DataPlane, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+dataplaneCols+` FROM dataplanes WHERE id = ?`, id)
	dp, err := scanDataPlane(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get data plane %q: %w", id, err)
	}
	return dp, nil
}

func (r *SQLiteRegistry) ListDataPlanes(ctx context.Context) ([]*DataPlane, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+dataplaneCols+` FROM dataplanes ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("registry: list data planes: %w", err)
	}
	defer rows.Close()
	var out []*DataPlane
	for rows.Next() {
		dp, err := scanDataPlane(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list data planes: scan: %w", err)
		}
		out = append(out, dp)
	}
	return out, rows.Err()
}

// UpdateDataPlane replaces the mutable fields (name, description, tier,
// gateway_url, status). JoinTokenHash and config_snapshot are managed by
// dedicated methods.
func (r *SQLiteRegistry) UpdateDataPlane(ctx context.Context, dp *DataPlane) error {
	dp.UpdatedAt = nowUTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE dataplanes
		   SET name = ?, description = ?, tier = ?, gateway_url = ?, status = ?, updated_at = ?
		 WHERE id = ?`,
		dp.Name, dp.Description, dp.Tier, dp.GatewayURL, dp.Status,
		fmtTime(dp.UpdatedAt), dp.ID)
	if err != nil {
		return fmt.Errorf("registry: update data plane %q: %w", dp.ID, err)
	}
	return mustAffect(res, "data plane", dp.ID)
}

func (r *SQLiteRegistry) DeleteDataPlane(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM dataplanes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("registry: delete data plane %q: %w", id, err)
	}
	return mustAffect(res, "data plane", id)
}

// ─── Heartbeat ────────────────────────────────────────────────────────────────

// RecordDataPlaneHeartbeat updates the status and last_heartbeat of a DP.
func (r *SQLiteRegistry) RecordDataPlaneHeartbeat(ctx context.Context, hb *DataPlaneHeartbeat) error {
	now := nowUTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE dataplanes SET status = ?, last_heartbeat = ?, updated_at = ? WHERE id = ?`,
		hb.Status, fmtTime(now), fmtTime(now), hb.DataPlaneID)
	if err != nil {
		return fmt.Errorf("registry: heartbeat data plane %q: %w", hb.DataPlaneID, err)
	}
	return mustAffect(res, "data plane", hb.DataPlaneID)
}

// ─── Config snapshot ──────────────────────────────────────────────────────────

func (r *SQLiteRegistry) GetDataPlaneConfigSnapshot(ctx context.Context, id string) (*DataPlaneConfigSnapshot, error) {
	var snapJSON sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT config_snapshot FROM dataplanes WHERE id = ?`, id).Scan(&snapJSON)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get config snapshot %q: %w", id, err)
	}
	snap := &DataPlaneConfigSnapshot{
		GeneratedAt:  nowUTC(),
		RoutingTable: map[string]any{},
		AuthBundle:   map[string]any{},
		PolicyBundle: []string{},
	}
	if snapJSON.Valid && snapJSON.String != "" && snapJSON.String != "{}" {
		if err := json.Unmarshal([]byte(snapJSON.String), snap); err != nil {
			return nil, fmt.Errorf("registry: decode config snapshot %q: %w", id, err)
		}
	}
	return snap, nil
}

func (r *SQLiteRegistry) UpdateDataPlaneConfigSnapshot(ctx context.Context, id string, snapshot *DataPlaneConfigSnapshot) error {
	b, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("registry: marshal config snapshot: %w", err)
	}
	now := nowUTC()
	res, dbErr := r.db.ExecContext(ctx, `
		UPDATE dataplanes SET config_snapshot = ?, updated_at = ? WHERE id = ?`,
		string(b), fmtTime(now), id)
	if dbErr != nil {
		return fmt.Errorf("registry: update config snapshot %q: %w", id, dbErr)
	}
	return mustAffect(res, "data plane", id)
}

// ─── Node assignment ──────────────────────────────────────────────────────────

// AssignNodeToDataPlane assigns a fleet node to a specific data plane.
// Pass dataplaneID="" to unassign.
func (r *SQLiteRegistry) AssignNodeToDataPlane(ctx context.Context, nodeID, dataplaneID string) error {
	var res sql.Result
	var err error
	if dataplaneID == "" {
		res, err = r.db.ExecContext(ctx,
			`UPDATE nodes SET dataplane_id = NULL, updated_at = ? WHERE id = ?`,
			fmtTime(nowUTC()), nodeID)
	} else {
		res, err = r.db.ExecContext(ctx,
			`UPDATE nodes SET dataplane_id = ?, updated_at = ? WHERE id = ?`,
			dataplaneID, fmtTime(nowUTC()), nodeID)
	}
	if err != nil {
		return fmt.Errorf("registry: assign node %q to data plane %q: %w", nodeID, dataplaneID, err)
	}
	return mustAffect(res, "node", nodeID)
}

// ListNodesByDataPlane returns nodes belonging to a specific data plane.
// When dataplaneID is "", nodes with no data plane assignment are returned.
func (r *SQLiteRegistry) ListNodesByDataPlane(ctx context.Context, dataplaneID string) ([]*Node, error) {
	var rows *sql.Rows
	var err error
	if dataplaneID == "" {
		rows, err = r.db.QueryContext(ctx,
			`SELECT `+nodeCols+` FROM nodes WHERE dataplane_id IS NULL ORDER BY hostname`)
	} else {
		rows, err = r.db.QueryContext(ctx,
			`SELECT `+nodeCols+` FROM nodes WHERE dataplane_id = ? ORDER BY hostname`,
			dataplaneID)
	}
	if err != nil {
		return nil, fmt.Errorf("registry: list nodes by data plane %q: %w", dataplaneID, err)
	}
	defer rows.Close()
	var out []*Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list nodes by data plane: scan: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ─── Token validation ─────────────────────────────────────────────────────────

// ValidateDataPlaneToken returns the DataPlane whose join_token_hash matches
// SHA-256(joinToken), or ErrNotFound. Used by the DP config-pull endpoint.
func (r *SQLiteRegistry) ValidateDataPlaneToken(ctx context.Context, joinToken string) (*DataPlane, error) {
	sum := sha256.Sum256([]byte(joinToken))
	hash := hex.EncodeToString(sum[:])
	row := r.db.QueryRowContext(ctx,
		`SELECT `+dataplaneCols+` FROM dataplanes WHERE join_token_hash = ?`, hash)
	dp, err := scanDataPlane(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: validate data plane token: %w", err)
	}
	return dp, nil
}
