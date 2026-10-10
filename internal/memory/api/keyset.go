package api

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/SumonMSelim/timothy/internal/memory/store"
)

const (
	defaultPageLimit = 50
	maxPageLimit     = 200
)

// parsePage reads ?limit= (default 50, max 200), ?before= (RFC3339Nano)
// and ?before_id= (UUID). The cursor halves travel together; same
// contract as brain's list endpoints.
func parsePage(q url.Values) (store.Page, error) {
	p := store.Page{Limit: defaultPageLimit, BeforeID: q.Get("before_id")}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPageLimit {
			return store.Page{}, fmt.Errorf("limit must be an integer between 1 and %d", maxPageLimit)
		}
		p.Limit = n
	}
	v := q.Get("before")
	switch {
	case v != "":
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return store.Page{}, errors.New("before must be an RFC3339Nano timestamp")
		}
		if p.BeforeID == "" {
			return store.Page{}, errors.New("before requires before_id")
		}
		if !uuidPattern.MatchString(p.BeforeID) {
			return store.Page{}, errors.New("before_id must be a UUID")
		}
		p.Before = t
	case p.BeforeID != "":
		return store.Page{}, errors.New("before_id requires before")
	}
	return p, nil
}
