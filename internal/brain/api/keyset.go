package api

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// keyset is a page cursor: the (timestamp, id) of the last row on the
// previous page. Both halves travel together so ties on the timestamp
// cannot drop or repeat rows. A zero Before means the first page.
type keyset struct {
	Before   time.Time
	BeforeID string
	Limit    int
}

// parseKeyset reads ?limit=, ?before= (RFC3339Nano) and ?before_id=.
// limit defaults to def and must be 1..max.
func parseKeyset(q url.Values, def, max int) (keyset, error) {
	k := keyset{Limit: def, BeforeID: q.Get("before_id")}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > max {
			return keyset{}, fmt.Errorf("limit must be an integer between 1 and %d", max)
		}
		k.Limit = n
	}
	v := q.Get("before")
	switch {
	case v != "":
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return keyset{}, errors.New("before must be an RFC3339Nano timestamp")
		}
		if k.BeforeID == "" {
			return keyset{}, errors.New("before requires before_id")
		}
		k.Before = t
	case k.BeforeID != "":
		return keyset{}, errors.New("before_id requires before")
	}
	return k, nil
}
