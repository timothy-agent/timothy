package api

import (
	"errors"
	"net/url"
	"strconv"
)

const (
	defaultSeqPageLimit = 200
	maxSeqPageLimit     = 500
)

// seqPage is a seq keyset window over an append-only event log (issue
// #1113). AfterSeq >= 0 selects seq > AfterSeq; otherwise BeforeSeq > 0
// selects seq < BeforeSeq and BeforeSeq == 0 the newest rows.
type seqPage struct {
	Limit     int
	BeforeSeq int64
	AfterSeq  int64
}

// parseSeqPage reads limit, before_seq and after_seq. paged is false
// when none is present: the caller serves the full log as before.
func parseSeqPage(q url.Values) (page seqPage, paged bool, err error) {
	page = seqPage{Limit: defaultSeqPageLimit, AfterSeq: -1}
	if q.Has("limit") {
		paged = true
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > maxSeqPageLimit {
			return seqPage{}, false, errors.New("limit must be an integer from 1 to 500")
		}
		page.Limit = n
	}
	if q.Has("before_seq") && q.Has("after_seq") {
		return seqPage{}, false, errors.New("before_seq and after_seq are mutually exclusive")
	}
	if q.Has("before_seq") {
		paged = true
		n, err := strconv.ParseInt(q.Get("before_seq"), 10, 64)
		if err != nil || n < 1 {
			return seqPage{}, false, errors.New("before_seq must be a positive integer")
		}
		page.BeforeSeq = n
	}
	if q.Has("after_seq") {
		paged = true
		n, err := strconv.ParseInt(q.Get("after_seq"), 10, 64)
		if err != nil || n < 0 {
			return seqPage{}, false, errors.New("after_seq must be a non-negative integer")
		}
		page.AfterSeq = n
	}
	return page, paged, nil
}
