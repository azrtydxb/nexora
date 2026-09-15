// Package assistant is the configuration assistant (M11 S-7): per-user sessions whose turns ask the model
// for a configuration change plan. A plan with actions becomes a config_assistant proposal the operator
// reviews and applies; nothing here changes configuration.
package assistant

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Session is one assistant conversation, owned by the principal that created it.
type Session struct {
	ID                        uuid.UUID
	OwnerID, OwnerKind, Title string
	CreatedAt, UpdatedAt      time.Time
}

// Message is one user or assistant message of a session.
type Message struct {
	ID                 int64
	SessionID          uuid.UUID
	Role, Content      string
	ProposalID, TaskID *uuid.UUID
	CreatedAt          time.Time
}

// titleRunes bounds the session title taken from the first user message.
const titleRunes = 80

// ownerID is the owner_id of p: the user id, or the token id for an API token (as ai_tasks.requested_by).
func ownerID(p auth.Principal) string {
	if p.Kind == "api_token" {
		return p.TokenID
	}
	return p.UserID
}

// CreateSession starts an empty session owned by p.
func CreateSession(ctx context.Context, st *store.Store, p auth.Principal) (Session, error) {
	s := Session{OwnerID: ownerID(p), OwnerKind: p.Kind}
	err := st.Pool.QueryRow(ctx, `insert into ai_assistant_sessions(owner_id, owner_kind) values ($1, $2)
		returning id, title, created_at, updated_at`, s.OwnerID, s.OwnerKind).Scan(&s.ID, &s.Title, &s.CreatedAt, &s.UpdatedAt)
	return s, store.MapError(err)
}

func getSession(ctx context.Context, st *store.Store, id uuid.UUID) (Session, error) {
	var s Session
	err := st.Pool.QueryRow(ctx, `select id, owner_id, owner_kind, title, created_at, updated_at from ai_assistant_sessions
		where id = $1`, id).Scan(&s.ID, &s.OwnerID, &s.OwnerKind, &s.Title, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, store.ErrNotFound
	}
	return s, store.MapError(err)
}

// GetSession returns a session of p with its messages oldest first; store.ErrNotFound when it is missing
// or owned by someone else.
func GetSession(ctx context.Context, st *store.Store, id uuid.UUID, p auth.Principal) (Session, []Message, error) {
	s, err := getSession(ctx, st, id)
	if err != nil {
		return Session{}, nil, err
	}
	if s.OwnerKind != p.Kind || s.OwnerID != ownerID(p) {
		return Session{}, nil, store.ErrNotFound
	}
	msgs, err := messages(ctx, st, id, 0, 0)
	return s, msgs, err
}

// messages returns the messages of a session oldest first: all of them, or with upTo > 0 the last limit
// messages with an id at most upTo.
func messages(ctx context.Context, st *store.Store, sessionID uuid.UUID, upTo int64, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 1 << 30
	}
	rows, err := st.Pool.Query(ctx, `select * from (
			select id, session_id, role, content, proposal_id, task_id, created_at from ai_assistant_messages
			where session_id = $1 and ($2 = 0 or id <= $2) order by id desc limit $3
		) m order by id`, sessionID, upTo, limit)
	if err != nil {
		return nil, store.MapError(err)
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.ProposalID, &m.TaskID, &m.CreatedAt); err != nil {
			return nil, store.MapError(err)
		}
		out = append(out, m)
	}
	return out, store.MapError(rows.Err())
}

// AddUserMessage appends a user message; the first one (cut to 80 characters) becomes the session title.
func AddUserMessage(ctx context.Context, st *store.Store, sessionID uuid.UUID, content string) (Message, error) {
	title := []rune(content)
	if len(title) > titleRunes {
		title = title[:titleRunes]
	}
	return addMessage(ctx, st, Message{SessionID: sessionID, Role: "user", Content: content}, string(title))
}

// LinkTask records the task that answers a user message.
func LinkTask(ctx context.Context, st *store.Store, messageID int64, taskID uuid.UUID) error {
	_, err := st.Pool.Exec(ctx, `update ai_assistant_messages set task_id = $2 where id = $1`, messageID, taskID)
	return store.MapError(err)
}

// LatestTaskID is the task of the newest message of a session that has one, or nil.
func LatestTaskID(ctx context.Context, st *store.Store, sessionID uuid.UUID) (*uuid.UUID, error) {
	var id *uuid.UUID
	err := st.Pool.QueryRow(ctx, `select task_id from ai_assistant_messages where session_id = $1 and task_id is not null
		order by id desc limit 1`, sessionID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return id, store.MapError(err)
}

// CurrentProposalID is the newest proposal of a session that was not superseded, or nil.
func CurrentProposalID(ctx context.Context, st *store.Store, sessionID uuid.UUID) (*uuid.UUID, error) {
	var id *uuid.UUID
	err := st.Pool.QueryRow(ctx, `select id from ai_proposals where session_id = $1 and status <> 'superseded'
		order by created_at desc, id limit 1`, sessionID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return id, store.MapError(err)
}

// addMessage inserts m and touches the session, setting its title when it has none.
func addMessage(ctx context.Context, st *store.Store, m Message, title string) (Message, error) {
	err := st.InTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update ai_assistant_sessions set updated_at = now(),
			title = case when title = '' then $2 else title end where id = $1`, m.SessionID, title)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return tx.QueryRow(ctx, `insert into ai_assistant_messages(session_id, role, content, proposal_id, task_id)
			values ($1, $2, $3, $4, $5) returning id, created_at`, m.SessionID, m.Role, m.Content, m.ProposalID, m.TaskID).
			Scan(&m.ID, &m.CreatedAt)
	})
	if err != nil {
		return Message{}, store.MapError(err)
	}
	return m, nil
}
