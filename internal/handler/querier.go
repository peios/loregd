package handler

import (
	"context"
	"database/sql"
)

// Querier is the common interface for database operations, satisfied
// by *sql.DB, *sql.Tx, and connQuerier (wrapping *sql.Conn).
type Querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// connQuerier wraps a pinned *sql.Conn to implement the Querier
// interface. Used for transaction-bound connections where BEGIN
// IMMEDIATE is executed manually.
type connQuerier struct {
	conn *sql.Conn
}

func (q *connQuerier) Exec(query string, args ...any) (sql.Result, error) {
	return q.conn.ExecContext(context.Background(), query, args...)
}

func (q *connQuerier) Query(query string, args ...any) (*sql.Rows, error) {
	return q.conn.QueryContext(context.Background(), query, args...)
}

func (q *connQuerier) QueryRow(query string, args ...any) *sql.Row {
	return q.conn.QueryRowContext(context.Background(), query, args...)
}
