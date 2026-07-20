package record

import (
	"hash/fnv"
	"strings"

	"github.com/guydelarea/tokentracer/internal/store"
)

// A subagent runs as its own API session, and nothing on the wire names its
// parent. The one thread between the two is the prompt: the parent's reply
// carries a Task/Agent tool_use block whose "prompt" field becomes, verbatim,
// the child session's first user message. The linker holds recently-seen spawn
// prompts just long enough to recognize the child when it arrives, and stamps
// the match onto the row as the parent_sid fact.
//
// The recorder's single worker means the order is guaranteed: a parent's
// exchange finishes (and is recorded) before the child it spawned finishes its
// first one. A prompt that never matches simply expires out of the ring, and a
// child that never matches stays its own row on the dashboard — the safe
// failure is an extra row, never a wrong merge.

// linkInfo is what the linker needs from an exchange, handed over by build():
// the request's whole first user text, and the spawn prompts the reply carried.
// Neither is ever persisted.
type linkInfo struct {
	firstText string
	spawned   []string
}

// linkerCap bounds both maps. 512 spawn prompts is hours of the heaviest
// multi-agent traffic; older entries are evicted first-in-first-out.
const linkerCap = 512

type linker struct {
	spawns map[uint64]string // spawn-prompt hash → the session whose reply carried it
	spawnQ []uint64          // insertion order, for eviction
	known  map[string]string // child session → parent, once matched
	knownQ []string
}

func newLinker() *linker {
	return &linker{spawns: map[uint64]string{}, known: map[string]string{}}
}

// apply resolves the row's parent, then registers any spawns its reply carried
// — in that order, so a session can never claim itself as its own parent.
func (l *linker) apply(row *store.Row, li linkInfo) {
	sid := row.SessionID
	if sid == "" {
		return
	}

	if p, ok := l.known[sid]; ok {
		row.ParentSid = p
	} else if li.firstText != "" {
		// Every request of a child session repeats the same first message, so a
		// child born before a proxy restart still can't match (the spawn map died
		// with the process) but one whose parent was recorded here always does.
		if p, ok := l.spawns[promptKey(li.firstText)]; ok && p != sid {
			row.ParentSid = p
			l.remember(sid, p)
		}
	}

	for _, prompt := range li.spawned {
		l.spawn(promptKey(prompt), sid)
	}
}

func (l *linker) spawn(key uint64, sid string) {
	if _, ok := l.spawns[key]; !ok {
		l.spawnQ = append(l.spawnQ, key)
		if len(l.spawnQ) > linkerCap {
			delete(l.spawns, l.spawnQ[0])
			l.spawnQ = l.spawnQ[1:]
		}
	}
	l.spawns[key] = sid
}

func (l *linker) remember(child, parent string) {
	if _, ok := l.known[child]; !ok {
		l.knownQ = append(l.knownQ, child)
		if len(l.knownQ) > linkerCap {
			delete(l.known, l.knownQ[0])
			l.knownQ = l.knownQ[1:]
		}
	}
	l.known[child] = parent
}

// promptKey hashes a prompt for the spawn map. Trimmed, because the client owns
// the incidental whitespace on each side and the two sides need not agree on it.
func promptKey(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(strings.TrimSpace(s)))
	return h.Sum64()
}
