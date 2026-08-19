package api

import (
	"log"
	"net/http"
	"time"

	"github.com/guydelarea/tokentracer/internal/store"
)

// retentionKey is the settings row the dashboard control writes.
const retentionKey = "retention"

// retentions is the whole vocabulary of the control: the choices the dashboard
// offers, and the windows they mean. It lives here rather than in the store
// because it is policy, and it is one map rather than two so the handler that
// validates a choice and the sweep that acts on it cannot disagree about what
// "7d" is.
//
// "off" is the default, and an unset setting reads as "" which maps to nothing
// — either way the sweep does nothing. Retention deletes evidence, so it only
// ever runs because someone asked for it.
var retentions = map[string]time.Duration{
	"off": 0,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// retentionOf reads the stored window. An unknown or unset value is "off": a
// setting we cannot interpret must not be interpreted as permission to delete.
func retentionOf(st *store.Store) string {
	v, err := st.Setting(retentionKey)
	if err != nil {
		log.Printf("tokentracer: reading retention: %v", err)
		return "off"
	}
	if _, ok := retentions[v]; !ok {
		return "off"
	}
	return v
}

// Sweep deletes the captures that have aged out of the stored retention
// window, and reports how many went. It is a plain function of (store, now) so
// the schedule that calls it is three lines of ticker with nothing to test, and
// the policy is here where it can be.
func Sweep(st *store.Store, now time.Time) (int64, error) {
	d := retentions[retentionOf(st)]
	if d == 0 {
		return 0, nil
	}
	return st.PruneCaptures(now.Add(-d).UnixMilli())
}

// settings writes the retention choice and acts on it immediately. Choosing
// "24 hours" has to mean yesterday's captures are gone now — waiting for the
// next hourly tick would look like the control did nothing.
func (s *server) settings(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query().Get(retentionKey)
	if _, ok := retentions[v]; !ok {
		http.Error(w, "unknown retention window", http.StatusBadRequest)
		return
	}
	if err := s.st.SetSetting(retentionKey, v); err != nil {
		serverError(w, "save retention", err)
		return
	}
	if n, err := Sweep(s.st, s.now()); err != nil {
		// The setting is saved; the sweep can be retried by the ticker. Report the
		// save, log the rest.
		log.Printf("tokentracer: sweep after retention change: %v", err)
	} else if n > 0 {
		log.Printf("tokentracer: retention set to %s — pruned %d captures", v, n)
	}
	w.WriteHeader(http.StatusNoContent)
}

// purge deletes every capture, now, regardless of the retention window. The
// fact rows are untouched — this reclaims disk, it does not erase history.
func (s *server) purge(w http.ResponseWriter, r *http.Request) {
	// Everything older than the next millisecond: all of it.
	n, err := s.st.PruneCaptures(s.now().Add(time.Millisecond).UnixMilli())
	if err != nil {
		serverError(w, "purge captures", err)
		return
	}
	log.Printf("tokentracer: purged %d captures", n)
	w.WriteHeader(http.StatusNoContent)
}

// storageOf is the disk footprint and the window governing it. Both are read on
// the stats path, so both fail soft: a broken read costs the header its readout,
// never the whole dashboard.
func (s *server) storageOf() storage {
	n, err := s.st.CaptureBytes()
	if err != nil {
		log.Printf("tokentracer: reading capture size: %v", err)
	}
	return storage{CaptureBytes: n, Retention: retentionOf(s.st)}
}
