package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	r "idp.local/reporting/reporting"
	"strconv"
)

// Verification of the rows against the object store after a restore (S04-F, docs/S04F_SCOPE.md F2). A restored database and a restored bucket are
// two copies made at two moments; the invariant that must hold between them is that a row which names an object has that object (a row must
// never promise a missing file). An object no row names is an orphan: harmless, and removed by the sweep's reconciliation after the grace.
//
// VerifyObjects reads and never writes: the rows in read-only transactions of the worker scope (SELECT on assets is all it needs), the bucket
// through List and, with deep, Get. It pages both sides in key order and joins them as a merge, so that memory is bounded by a page whatever the
// store holds. It returns counts, and the ids of the rows that fail so that an operator restoring a bucket can find them; the command prints the
// ids only when asked.

// VerifyResult is what a verification found.
type VerifyResult struct {
	Rows       int  // artifact rows that name an object
	Objects    int  // objects under the artifacts/ prefix that are ObjectKey of an id (what the sweep's reconciliation considers its own)
	Missing    int  // rows whose object is absent
	Orphans    int  // objects no row names (informational)
	Invalid    int  // rows that do not say, exactly as ValidArtifactBody requires, where their file is, or whose payload fails its digest
	Mismatched int  // deep only: rows whose object's length or SHA-256 is not the row's
	Deep       bool // whether the bytes were read

	MissingIDs, MismatchedIDs, InvalidIDs []string // the artifact ids of the above, at most MaxVerifyIDs each
}

// MaxVerifyIDs bounds each list of ids a verification keeps.
const MaxVerifyIDs = 10000

// Failed says whether the store is not what the rows promise: a row without its object, with other bytes, or that is not valid.
func (v VerifyResult) Failed() bool { return v.Missing > 0 || v.Mismatched > 0 || v.Invalid > 0 }

// verifyRow is one artifact row that names an object, as the verification reads it.
type verifyRow struct {
	id      string
	valid   bool
	key     string
	size    int
	sha256  string
	canonID bool
}

const verifyPage = 1000

// VerifyObjects checks every artifact row that names an object against the bucket (see above). deep also reads every object once, at most
// MaxArtifactBytes each, and compares its length and SHA-256 with the row's; without it only the presence of the objects is checked. An object
// store that cannot be reached is an error, not a count.
func VerifyObjects(ctx context.Context, pool *pgxpool.Pool, objects r.ObjectStore, deep bool) (VerifyResult, error) {
	res := VerifyResult{Deep: deep}
	if objects == nil {
		return res, fmt.Errorf("verification needs an object store: %w", r.ErrInvalid)
	}
	rows := &rowStream{ctx: ctx, pool: pool}
	keys := &keyStream{ctx: ctx, objects: objects}
	add := func(list *[]string, id string) {
		if len(*list) < MaxVerifyIDs {
			*list = append(*list, id)
		}
	}
	// The join: for each row in key order, the objects before its key are orphans; the object at its key, if any, is its own.
	for {
		row, ok, e := rows.next()
		if e != nil {
			return res, e
		}
		if !ok {
			break
		}
		res.Rows++
		if !row.valid {
			res.Invalid++
			add(&res.InvalidIDs, row.id)
		}
		if !row.canonID {
			// An id that is not canonical has no key to look for; its row is invalid and takes no part in the join.
			continue
		}
		want := r.ObjectKey(row.id)
		for {
			k, ok, e := keys.peek()
			if e != nil {
				return res, e
			}
			if !ok || k >= want {
				break
			}
			res.Objects++
			res.Orphans++
			keys.advance()
		}
		k, ok, e := keys.peek()
		if e != nil {
			return res, e
		}
		if !ok || k != want {
			res.Missing++
			add(&res.MissingIDs, row.id)
			continue
		}
		res.Objects++
		keys.advance()
		if deep && row.valid {
			_, e := objects.Get(ctx, want, row.sha256, row.size)
			switch {
			case e == nil:
			case errors.Is(e, r.ErrIntegrity):
				res.Mismatched++
				add(&res.MismatchedIDs, row.id)
			default:
				return res, e
			}
		}
	}
	// The objects after the last row are orphans too.
	for {
		_, ok, e := keys.peek()
		if e != nil {
			return res, e
		}
		if !ok {
			return res, nil
		}
		res.Objects++
		res.Orphans++
		keys.advance()
	}
}

// rowStream pages the artifact rows that name an object by id. The ids are canonical lowercase hexadecimal, which every collation of the
// database orders as the bucket orders the keys; a stream that arrives out of order is refused rather than joined wrongly.
type rowStream struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	after string
	last  string
	page  []verifyRow
	done  bool
}

// verifyRows reads one page. The payload's digest is checked by the database (the store's decoding does the same on a read); Content is
// reported only as present or not, because the verification never needs the bytes of an inline file.
const verifyRows = `SELECT id, data->>'Object', data->>'SHA256', data->>'Size',
coalesce(jsonb_typeof(data->'Content')='string' AND data->>'Content' <> '', false),
data_sha256 = encode(sha256(convert_to(data::text,'UTF8')),'hex')
FROM reporting_s01.assets
WHERE kind='artifact' AND coalesce(data->>'Object','') <> '' AND id > $1
ORDER BY id LIMIT $2`

func (s *rowStream) next() (verifyRow, bool, error) {
	for len(s.page) == 0 {
		if s.done {
			return verifyRow{}, false, nil
		}
		if e := s.ctx.Err(); e != nil {
			return verifyRow{}, false, e
		}
		var page []verifyRow
		tx, e := beginWorkerReadOnly(s.ctx, s.pool)
		if e != nil {
			return verifyRow{}, false, e
		}
		cur, e := tx.Query(s.ctx, verifyRows, s.after, verifyPage)
		if e == nil {
			for cur.Next() {
				var id, sha string
				var object, shaCol, size *string
				var hasContent, digestOK bool
				if e = cur.Scan(&id, &object, &shaCol, &size, &hasContent, &digestOK); e != nil {
					break
				}
				if shaCol != nil {
					sha = *shaCol
				}
				_, canon := r.ObjectID(r.ObjectKey(id))
				row := verifyRow{id: id, canonID: canon, sha256: sha}
				n := 0
				if size != nil {
					n, _ = strconv.Atoi(*size)
				}
				row.size = n
				a := r.Artifact{ID: id, Object: *object, Size: n, SHA256: sha}
				row.valid = digestOK && !hasContent && row.canonID && r.ValidArtifactBody(a)
				if canon {
					if id <= s.last {
						e = fmt.Errorf("the database's order of ids is not the bucket's key order (collation %q?): %w", "C expected for hexadecimal ids", r.ErrIntegrity)
						break
					}
					s.last = id
				}
				page = append(page, row)
			}
			if e == nil {
				e = cur.Err()
			}
			cur.Close()
		}
		rollback(s.ctx, tx)
		if e != nil {
			return verifyRow{}, false, e
		}
		if len(page) < verifyPage {
			s.done = true
		}
		if len(page) > 0 {
			s.after = page[len(page)-1].id
		}
		s.page = page
	}
	row := s.page[0]
	s.page = s.page[1:]
	return row, true, nil
}

// keyStream pages the bucket's artifacts/ prefix, yielding the keys that are ObjectKey of an id (a key that is not is another process's and is
// not counted, as the reconciliation ignores it).
type keyStream struct {
	ctx     context.Context
	objects r.ObjectStore
	next    string
	done    bool
	page    []string
	last    string
}

func (s *keyStream) peek() (string, bool, error) {
	for len(s.page) == 0 {
		if s.done {
			return "", false, nil
		}
		if e := s.ctx.Err(); e != nil {
			return "", false, e
		}
		page, next, e := s.objects.List(s.ctx, r.ObjectPrefix, s.next, listPage)
		if e != nil {
			return "", false, e
		}
		for _, o := range page {
			if _, ok := r.ObjectID(o.Key); !ok {
				continue
			}
			if o.Key <= s.last {
				return "", false, fmt.Errorf("the object store's listing is not in key order: %w", r.ErrIntegrity)
			}
			s.last = o.Key
			s.page = append(s.page, o.Key)
		}
		if next == "" {
			s.done = true
		}
		s.next = next
	}
	return s.page[0], true, nil
}

func (s *keyStream) advance() { s.page = s.page[1:] }
