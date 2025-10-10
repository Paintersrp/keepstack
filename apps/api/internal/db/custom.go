package db

import (
	"context"
)

type UpdateTagParams struct {
	ID   int32
	Name string
}

const updateTagQuery = `UPDATE tags SET name = $1 WHERE id = $2 RETURNING id, name`

func (q *Queries) UpdateTag(ctx context.Context, arg UpdateTagParams) (Tag, error) {
	row := q.db.QueryRow(ctx, updateTagQuery, arg.Name, arg.ID)
	var tag Tag
	err := row.Scan(&tag.ID, &tag.Name)
	return tag, err
}
