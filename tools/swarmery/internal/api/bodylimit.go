package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// maxJSONBody is the default cap on a JSON request body. Every handler that
// decodes one through decodeJSONLimited gets it unless its payload genuinely
// needs more; the dashboard's largest ordinary body (a composer message, a hook
// edit) is a few KiB.
const maxJSONBody = 1 << 20

// decodeJSONLimited decodes one JSON value from r.Body, reading at most max
// bytes. A body past the cap gets a 413 written here and its error returned, so
// the caller only returns; isBodyTooLarge tells the two apart. Any other decode
// error is returned unwritten, and each caller answers it with its own 400 —
// the error shapes the endpoints already promise stay unchanged.
func decodeJSONLimited(w http.ResponseWriter, r *http.Request, v any, max int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	err := json.NewDecoder(r.Body).Decode(v)
	if isBodyTooLarge(err) {
		writeClientErr(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("request body exceeds %d bytes", max))
	}
	return err
}

// isBodyTooLarge reports whether err came from a body past its cap (the 413
// decodeJSONLimited has already written).
func isBodyTooLarge(err error) bool {
	var tooBig *http.MaxBytesError
	return errors.As(err, &tooBig)
}
