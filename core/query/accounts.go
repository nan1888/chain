package query

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"

	"chain/core/query/filter"
	"chain/errors"
)

// SaveAnnotatedAccount saves an annotated account to the query indexes.
func (ind *Indexer) SaveAnnotatedAccount(ctx context.Context, account *AnnotatedAccount) error {
	keysJSON, err := json.Marshal(account.Keys)
	if err != nil {
		return errors.Wrap(err)
	}
	alias := sql.NullString{String: account.Alias, Valid: account.Alias != ""}

	const q = `
		INSERT INTO annotated_accounts (id, alias, keys, quorum, tags)
		VALUES($1, $2, $3::jsonb, $4, $5::jsonb)
		ON CONFLICT (id) DO UPDATE SET tags = $5::jsonb
	`
	_, err = ind.db.Exec(ctx, q, account.ID, alias, keysJSON,
		account.Quorum, string(*account.Tags))
	return errors.Wrap(err, "saving annotated account")
}

// Accounts queries the blockchain for accounts matching the query `q`.
func (ind *Indexer) Accounts(ctx context.Context, p filter.Predicate, vals []interface{}, after string, limit int) ([]*AnnotatedAccount, string, error) {
	if len(vals) != p.Parameters {
		return nil, "", ErrParameterCountMismatch
	}
	expr, err := filter.AsSQL(p, accountsTable, vals)
	if err != nil {
		return nil, "", errors.Wrap(err, "converting to SQL")
	}

	queryStr, queryArgs := constructAccountsQuery(expr, vals, after, limit)
	rows, err := ind.db.Query(ctx, queryStr, queryArgs...)
	if err != nil {
		return nil, "", errors.Wrap(err, "executing acc query")
	}
	defer rows.Close()

	accounts := make([]*AnnotatedAccount, 0, limit)
	for rows.Next() {
		var keysJSON []byte
		aa := new(AnnotatedAccount)

		err := rows.Scan(
			&aa.ID,
			&aa.Alias,
			&keysJSON,
			&aa.Quorum,
			&aa.Tags,
		)
		if err != nil {
			return nil, "", errors.Wrap(err, "scanning account row")
		}
		err = json.Unmarshal(keysJSON, &aa.Keys)
		if err != nil {
			return nil, "", errors.Wrap(err, "unmarshaling account keys json")
		}

		after = aa.ID
		accounts = append(accounts, aa)
	}
	return accounts, after, errors.Wrap(rows.Err())
}

func constructAccountsQuery(expr string, vals []interface{}, after string, limit int) (string, []interface{}) {
	var buf bytes.Buffer

	buf.WriteString("SELECT ")
	buf.WriteString("id, alias, keys, quorum, tags")
	buf.WriteString(" FROM annotated_accounts AS acc")
	buf.WriteString(" WHERE ")

	// add filter conditions
	if len(expr) > 0 {
		buf.WriteString("(")
		buf.WriteString(expr)
		buf.WriteString(") AND ")
	}

	// add after conditions
	buf.WriteString(fmt.Sprintf("($%d='' OR id < $%d) ", len(vals)+1, len(vals)+1))
	vals = append(vals, after)

	buf.WriteString("ORDER BY id DESC ")
	buf.WriteString("LIMIT " + strconv.Itoa(limit))
	return buf.String(), vals
}
