package domain

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrUpstreamTimeout = errors.New("upstream timeout")
	ErrESPNAuth        = errors.New("espn authentication failed")
)

// UpstreamError is a failed call to a provider: a non-2xx status, an unparseable
// body (BadBody), or a transport error (Status 0).
type UpstreamError struct {
	Provider string
	Status   int
	BadBody  bool
	Err      error
}

func (e *UpstreamError) Error() string {
	msg := "upstream " + e.Provider
	if e.Status != 0 {
		msg += fmt.Sprintf(" status %d", e.Status)
	}
	if e.BadBody {
		msg += " returned an unparseable body"
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *UpstreamError) Unwrap() error { return e.Err }

type InvalidParamError struct {
	Param  string
	Reason string
}

func (e *InvalidParamError) Error() string { return e.Param + ": " + e.Reason }
