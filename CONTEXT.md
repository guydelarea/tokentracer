# TokenTracer

A local proxy between an AI coding tool and its LLM API that records what every request actually cost and shows where the money leaks. Facts are recorded verbatim from the wire; interpretations are computed at read time.

## Language

**Exchange**:
One complete round-trip through the proxy — request, response, and timing. The unit of recording.
_Avoid_: request (that's the wire message), trace, call

**Recorder**:
The module that turns Exchanges into facts and captures. Owns the guarantee that no Exchange it receives is ever lost.
_Avoid_: sink worker, logger, ingester

**Fact**:
A value copied verbatim from the wire onto an Exchange's row — tokens, model, status, latency, byte sizes. Never derived, never priced.
_Avoid_: metric, stat

**Capture**:
The verbatim request body and assembled response kept alongside a row for drill-down. Deletable without touching facts.
_Avoid_: log, dump, payload

**Breakdown**:
The read-time itemization of a capture — every tool schema, system block, and message named and sized.
_Avoid_: analysis, report

**Degradation ladder**:
The ordered fallbacks that keep a row worth having when recording goes wrong: parse failure, panic, oversize, dropped insert. Each rung preserves more than the one below it.

**Aborted Exchange**:
An Exchange whose client hung up before the response finished. The generated tokens were still billed, so it is still recorded in full.
_Avoid_: cancelled request, failed request

**Unpriced**:
An Exchange whose model has no known rate. Surfaced as a badge and a counter — never a silent $0.

**Bill**:
The read-time pricing of an Exchange's usage facts against the rate table. An interpretation, computed fresh on every read.
_Avoid_: cost column, stored cost

**Fold**:
The read-time computation that turns facts and rates into every number the dashboard shows. Facts stay in the database; the fold is where they become meaning.
_Avoid_: aggregation pipeline, stats engine

**Vendor**:
An upstream API dialect (v1: the Anthropic Messages API). Each vendor is one self-contained package.
_Avoid_: provider, backend

**Session**:
One Claude Code conversation, identified by the session id embedded in request metadata.
