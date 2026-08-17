// Command tokentracer is a local proxy that records what every LLM request
// actually cost, and shows where the money leaks.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/guydelarea/tokentracer/internal/api"
	"github.com/guydelarea/tokentracer/internal/proxy"
	"github.com/guydelarea/tokentracer/internal/record"
	"github.com/guydelarea/tokentracer/internal/store"
	"github.com/guydelarea/tokentracer/internal/upstream"
	"github.com/guydelarea/tokentracer/internal/wire"
)

// shutdownGrace bounds the whole shutdown: in-flight streams, the proxy's 30s
// abort drains, the recorder's queue. A blown deadline is WAL-crash-equivalent,
// which SQLite survives — but the tail of a session is where the interesting
// requests are, so we wait for it.
const shutdownGrace = 35 * time.Second

// banner is tokentrace's ANSI Shadow wordmark, extended with the trailing R.
const banner = `
████████╗ ██████╗ ██╗  ██╗███████╗███╗   ██╗████████╗██████╗  █████╗  ██████╗███████╗██████╗
╚══██╔══╝██╔═══██╗██║ ██╔╝██╔════╝████╗  ██║╚══██╔══╝██╔══██╗██╔══██╗██╔════╝██╔════╝██╔══██╗
   ██║   ██║   ██║█████╔╝ █████╗  ██╔██╗ ██║   ██║   ██████╔╝███████║██║     █████╗  ██████╔╝
   ██║   ██║   ██║██╔═██╗ ██╔══╝  ██║╚██╗██║   ██║   ██╔══██╗██╔══██║██║     ██╔══╝  ██╔══██╗
   ██║   ╚██████╔╝██║  ██╗███████╗██║ ╚████║   ██║   ██║  ██║██║  ██║╚██████╗███████╗██║  ██║
   ╚═╝    ╚═════╝ ╚═╝  ╚═╝╚══════╝╚═╝  ╚═══╝   ╚═╝   ╚═╝  ╚═╝╚═╝  ╚═╝ ╚═════╝╚══════╝╚═╝  ╚═╝
`

type config struct {
	Port   string
	Routes []upstream.Route
	DBPath string
}

// Upstream is the first route's base URL — what a single-upstream install has
// always called "the upstream", and what the dashboard header shows when there
// is only one.
func (c config) Upstream() string {
	if len(c.Routes) == 0 {
		return ""
	}
	return c.Routes[0].Base()
}

// loadConfig reads the routing config, newest key first. UPSTREAMS carries a
// list of name=url pairs; UPSTREAM carries the single value TokenTracer shipped
// with, and is still what a one-client install writes. A malformed list is fatal
// rather than silently narrowed: half a route table would send some client's
// traffic somewhere the user never named.
func loadConfig() (config, error) {
	spec := env("UPSTREAMS", "")
	if spec == "" {
		spec = env("UPSTREAM", "https://api.anthropic.com")
	}
	routes, err := upstream.Parse(spec)
	if err != nil {
		return config{}, err
	}
	return config{
		Port:   env("PORT", "8787"),
		Routes: upstream.Dedupe(routes),
		DBPath: env("TOKENTRACER_DB", "./tokentracer.db"),
	}, nil
}

// envFile is where the setup wizard saves its answer, next to the DB in cwd.
const envFile = ".env"

// loadEnvFile sets KEY=value pairs from path into the process env, but never
// overrides a variable the shell already set — the shell is the louder opinion.
func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

func writeEnvFile(path string, kv map[string]string) error {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# tokentracer config — edit, or re-run `" + setupHint() + "`\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, kv[k])
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// vertexUpstream is the Google endpoint a region implies — including the /v1
// that Claude Code's Vertex paths do not carry.
func vertexUpstream(region string) string {
	if region == "" || region == "global" {
		return "https://aiplatform.googleapis.com/v1"
	}
	return fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1", region)
}

// liteLLMDefault is where a LiteLLM proxy listens out of the box.
const liteLLMDefault = "http://localhost:4000"

// chooseUpstreams runs the wizard's questions through ask and returns the
// upstreams the answers imply, in the order they were chosen. Order is not
// cosmetic: when two upstreams could serve the same request, the earlier one
// wins, so the first answer is the default for its dialect.
//
// The answer is a list because one proxy now fronts as many APIs as the user
// runs clients — "1,5" is Claude Code on Anthropic and OpenCode on OpenAI in
// the same dashboard.
func chooseUpstreams(ask func(prompt string) string) []upstream.Route {
	fmt.Println("tokentracer: first-run setup — which clients? (comma-separated, e.g. 1,5)")
	fmt.Println("  1) Claude Code / Pi — Anthropic API (default)")
	fmt.Println("  2) Claude Code — Vertex AI")
	fmt.Println("  3) Claude Code — LiteLLM or another gateway speaking the Anthropic API")
	fmt.Println("  4) Codex / OpenCode / Pi — ChatGPT login / OAuth (chatgpt.com)")
	fmt.Println("  5) Codex / OpenCode / Pi — OpenAI API key (api.openai.com; not ChatGPT OAuth)")
	fmt.Println("  6) Other — paste an upstream base URL")

	answer := ask("Choice [1]: ")

	var routes []upstream.Route
	for _, choice := range splitChoices(answer) {
		base, name := upstreamFor(choice, ask)
		if base == "" {
			continue
		}
		routes = append(routes, namedRoute(base, name))
	}
	if len(routes) == 0 {
		routes = append(routes, namedRoute("https://api.anthropic.com", ""))
	}
	return upstream.Dedupe(routes)
}

// splitChoices reads "1,5" or "1 5" or "1" the same way. An empty answer is one
// empty choice, which upstreamFor turns into the default — the behaviour of
// pressing enter, unchanged.
func splitChoices(answer string) []string {
	fields := strings.FieldsFunc(answer, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	if len(fields) == 0 {
		return []string{""}
	}
	return fields
}

// upstreamFor turns one menu choice into a base URL, asking the follow-up that
// choice needs. It also returns the route's name where the MENU knows the
// dialect but the URL cannot show it: option 3 is a gateway that speaks the
// Messages API, and calling that route "anthropic" is how the router learns
// what http://localhost:4000 is. "" means "name it after its host".
func upstreamFor(choice string, ask func(prompt string) string) (base, name string) {
	switch choice {
	case "2":
		return vertexUpstream(ask("Vertex region, e.g. us-east5 (blank = global): ")), ""
	case "3":
		if up := gatewayUpstream(ask("LiteLLM base URL [" + liteLLMDefault + "]: ")); up != "" {
			return up, "anthropic"
		}
		return liteLLMDefault, "anthropic"
	case "4":
		return "https://chatgpt.com/backend-api/codex", ""
	case "5":
		return "https://api.openai.com/v1", ""
	case "6":
		if up := gatewayUpstream(ask("Upstream base URL: ")); up != "" {
			return up, ""
		}
		return "https://api.anthropic.com", ""
	default:
		return "https://api.anthropic.com", ""
	}
}

// namedRoute gives a base URL the short name it is known by — on the dashboard,
// in the launch lines, and in the /tt/<name>/ prefix. An empty name falls back
// to what the URL implies.
func namedRoute(base, name string) upstream.Route {
	routes, err := upstream.Parse(base)
	if err != nil || len(routes) == 0 {
		return upstream.Route{}
	}
	r := routes[0]
	if name != "" {
		r.Name = name
	}
	return r
}

// gatewayUpstream normalizes a pasted base URL: a bare host:port gets the http
// scheme a local gateway is served on, and the trailing slash goes, since every
// client path is appended to this one.
func gatewayUpstream(answer string) string {
	answer = strings.TrimSuffix(strings.TrimSpace(answer), "/")
	if answer == "" {
		return ""
	}
	if !strings.Contains(answer, "://") {
		answer = "http://" + answer
	}
	return answer
}

// runSetup asks which client this proxy fronts and saves the upstream that
// answer implies.
func runSetup() {
	r := bufio.NewReader(os.Stdin)
	// /dev/null passes the TTY check (it is a char device), so EOF is the real
	// "nobody is answering" signal: default everything and persist nothing.
	interactive := true
	ask := func(prompt string) string {
		fmt.Print(prompt)
		s, err := r.ReadString('\n')
		if err != nil {
			interactive = false
		}
		return strings.TrimSpace(s)
	}

	spec := upstream.Format(chooseUpstreams(ask))

	if interactive {
		if err := writeEnvFile(envFile, map[string]string{"UPSTREAMS": spec}); err != nil {
			fmt.Fprintf(os.Stderr, "tokentracer: could not write %s: %v\n", envFile, err)
		} else {
			fmt.Printf("tokentracer: saved %s — re-run `%s` to change it\n", envFile, setupHint())
		}
	}
	// UPSTREAMS outranks whatever UPSTREAM a previous setup left behind, so the
	// answer just given is the one this process runs with.
	os.Setenv("UPSTREAMS", spec)
}

// shellOnlyUpstream reports that the shell — not .env — is carrying the legacy
// single-upstream key, and nothing newer. UPSTREAMS outranks UPSTREAM
// everywhere else, but a shell-set variable outranks the file, and both rules
// meet only here: an `UPSTREAM=… tokentracer` in front of an install whose .env
// holds a saved list is a deliberate one-off override, not a stale key.
func shellOnlyUpstream() bool {
	return os.Getenv("UPSTREAM") != "" && os.Getenv("UPSTREAMS") == ""
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// launchCmd is one copy-pasteable way to put a client behind the proxy: the
// client's name, the command to run, and — when the command alone is not the
// whole story — the note that completes it (which credential the upstream
// takes, which config file to touch first).
type launchCmd struct {
	Client string
	Note   string
	Cmd    string // "" when the client is configured in a file, not launched with a flag
}

// launchBlock is the launch commands for one upstream. Label and Notice exist
// only when they disambiguate: the label names the route when several share the
// port, and the notice warns about the one thing a command cannot show — which
// credential the upstream will actually accept.
type launchBlock struct {
	Label  string
	Notice string
	Cmds   []launchCmd
}

// launchBlocks are the copy-paste commands that point clients at the proxy,
// one block per configured upstream.
//
// Each block gets the URL that actually reaches its own upstream. A client whose
// dialect already routes there uses the bare proxy URL and needs to know nothing
// about routing; only a client that would collide with an earlier route — Codex
// and OpenCode both speaking Responses to different hosts — gets the /tt/<name>/
// prefix that settles it.
func launchBlocks(cfg config) []launchBlock {
	table, err := upstream.New(cfg.Routes)
	if err != nil {
		return nil
	}

	var out []launchBlock
	for _, r := range cfg.Routes {
		base := "http://localhost:" + cfg.Port
		label := ""
		if len(cfg.Routes) > 1 {
			base += routeSuffix(table, r)
			label = r.Name + " → " + r.Base()
		}
		out = append(out, launchBlock{
			Label:  label,
			Notice: authNotice(r.Base()),
			Cmds:   clientCmds(r, base),
		})
	}
	return out
}

// routeSuffix is "" when this route is what detection would pick anyway, and
// "/tt/<name>" when it is not. The check is per dialect the route can serve:
// a route that is the default for none of them is only reachable by name.
func routeSuffix(table *upstream.Table, r upstream.Route) string {
	for _, k := range []wire.Kind{wire.Anthropic, wire.OpenAI, wire.OpenAIChat} {
		if !r.Serves(k) {
			continue
		}
		if def, ok := table.Default(k); ok && def.Name == r.Name {
			return ""
		}
	}
	return upstream.Prefix + r.Name
}

// clientCmds are the per-client commands for one upstream: what to set, given
// the wire family that upstream speaks, pointed at the URL that reaches it.
//
// The vendor hosts are matched first because they fix both halves of the
// answer — the dialect AND whether the key is a vendor's. A gateway fixes only
// the first, and only if the route was named for it, so it still gets the
// ANTHROPIC_AUTH_TOKEN reminder: pointed at a gateway, Claude Code sends that
// token as a bearer, and it is the gateway's key, not Anthropic's.
func clientCmds(r upstream.Route, base string) []launchCmd {
	upstreamURL := r.Base()
	if strings.Contains(upstreamURL, "googleapis") {
		return []launchCmd{{Client: "Claude Code", Cmd: "CLAUDE_CODE_USE_VERTEX=1 ANTHROPIC_VERTEX_BASE_URL=" + base + " claude"}}
	}
	if auth, provider, ok := openAIAuth(upstreamURL); ok {
		return openAICmds(auth, provider, base)
	}
	if strings.Contains(upstreamURL, "api.anthropic.com") {
		return []launchCmd{
			{Client: "Claude Code", Cmd: "ANTHROPIC_BASE_URL=" + base + " claude"},
			opencodeCmd("anthropic", "", base),
			piCmd("anthropic", base),
		}
	}
	// A gateway that was named for a dialect — "anthropic=http://localhost:4000"
	// is how setup records LiteLLM on the Messages API — gets that dialect's
	// clients and no others. Printing the OpenAI commands for it would be
	// printing a 404.
	switch r.Dialect() {
	case "anthropic":
		return []launchCmd{
			{Client: "Claude Code", Note: "the token is the gateway's key", Cmd: "ANTHROPIC_BASE_URL=" + base + " ANTHROPIC_AUTH_TOKEN=<gateway key> claude"},
			opencodeCmd("anthropic", "", base),
			piCmd("anthropic", base),
		}
	case "openai", "codex":
		// A gateway's credential is the gateway's, so there is no vendor auth to
		// name — that distinction only exists between OpenAI's own two hosts.
		return openAICmds("", "openai", base)
	}
	// Anything else is a gateway that declared nothing: LiteLLM, another company
	// proxy, an aggregator. One base URL there serves every dialect it was
	// configured for, and TokenTracer records whichever one the client speaks —
	// so print a command per client rather than guessing.
	return []launchCmd{
		{Client: "Claude Code", Note: "the token is the gateway's key", Cmd: "ANTHROPIC_BASE_URL=" + base + " ANTHROPIC_AUTH_TOKEN=<gateway key> claude"},
		{Client: "Codex", Cmd: `codex -c 'openai_base_url="` + base + `"'`},
		{Client: "OpenCode / Pi", Note: "set the selected provider's base URL to " + base},
	}
}

// openAICmds are the OpenAI-dialect clients pointed at one upstream. auth names
// which credential that upstream takes, when the upstream is one of OpenAI's own
// two hosts and the answer is therefore knowable — the hosts are not
// interchangeable, and sending the wrong one fails with a message that does not
// say so. A gateway takes its own key, so it passes "".
func openAICmds(auth, provider, base string) []launchCmd {
	return []launchCmd{
		{Client: "Codex", Note: auth, Cmd: `codex -c 'openai_base_url="` + base + `"'`},
		opencodeCmd("openai", auth, base),
		piCmd(provider, base),
	}
}

func opencodeCmd(provider, note, base string) launchCmd {
	return launchCmd{
		Client: "OpenCode",
		Note:   note,
		Cmd:    fmt.Sprintf(`OPENCODE_CONFIG_CONTENT='{"provider":{"%s":{"options":{"baseURL":"%s"}}}}' opencode`, provider, base),
	}
}

func piCmd(provider, base string) launchCmd {
	return launchCmd{
		Client: "Pi",
		Note:   fmt.Sprintf(`first set ~/.pi/agent/models.json providers.%s.baseUrl="%s"`, provider, base),
		Cmd:    "pi --provider " + provider,
	}
}

func openAIAuth(upstreamURL string) (auth, provider string, ok bool) {
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return "", "", false
	}
	switch {
	case strings.EqualFold(u.Hostname(), "chatgpt.com") && strings.Contains(u.Path, "/backend-api/codex"):
		return "ChatGPT login/OAuth", "openai-codex", true
	case strings.EqualFold(u.Hostname(), "api.openai.com"):
		return "OpenAI API key", "openai", true
	default:
		return "", "", false
	}
}

func authNotice(upstreamURL string) string {
	_, provider, ok := openAIAuth(upstreamURL)
	if !ok {
		return ""
	}
	if provider == "openai-codex" {
		return "ChatGPT login/OAuth required; do not use an OpenAI API key"
	}
	return `OpenAI API key required; ChatGPT OAuth will fail with "Missing scopes: api.responses.write"`
}

// setupHint is the command that re-runs the wizard, phrased for how this
// process was actually started: `go run` builds into a go-build temp dir, an
// installed binary answers to its own name.
func setupHint() string {
	if strings.Contains(os.Args[0], "go-build") {
		return "go run ./cmd/tokentracer setup"
	}
	return filepath.Base(os.Args[0]) + " setup"
}

// startupScreen is everything the user needs after the banner: where the proxy
// and dashboard are, which config produced that answer, and the exact command
// that launches each client through the proxy — ready to paste, not buried in
// log lines.
func startupScreen(cfg config, source string) string {
	var b strings.Builder
	base := "http://localhost:" + cfg.Port

	// One route fits on the line; several are already itemized below, so the
	// summary only says how many share the port.
	target := cfg.Upstream()
	if len(cfg.Routes) > 1 {
		target = fmt.Sprintf("%d upstreams, routed by dialect (below)", len(cfg.Routes))
	}
	fmt.Fprintf(&b, "\n  Proxy      %s → %s\n", base, target)
	fmt.Fprintf(&b, "  Dashboard  %s/dashboard\n", base)
	fmt.Fprintf(&b, "  Database   %s\n", cfg.DBPath)
	fmt.Fprintf(&b, "  Config     %s — change with `%s`\n", source, setupHint())

	b.WriteString("\n  Ready. Launch your agent through the proxy — pick your client:\n")
	for _, blk := range launchBlocks(cfg) {
		if blk.Label != "" {
			fmt.Fprintf(&b, "\n  ── %s\n", blk.Label)
		}
		if blk.Notice != "" {
			fmt.Fprintf(&b, "\n  ⚠ %s\n", blk.Notice)
		}
		for _, c := range blk.Cmds {
			header := "  " + c.Client
			if c.Note != "" {
				header += " — " + c.Note
			}
			b.WriteString("\n" + header + "\n")
			if c.Cmd != "" {
				b.WriteString("  $ " + c.Cmd + "\n")
			}
		}
	}
	b.WriteString("\n")
	return b.String()
}

// configSource names where the running config came from, for the startup
// screen: the shell outranks .env, which outranks the built-in defaults —
// the same precedence loadConfig applies, said out loud.
func configSource(fromShell bool) string {
	if fromShell {
		return "shell environment (outranks " + envFile + ")"
	}
	if _, err := os.Stat(envFile); err == nil {
		return envFile + " (applied)"
	}
	return "defaults (nothing saved yet)"
}

// upstreamViews is the route table as the dashboard shows it, carrying the
// same "bare URL or /tt/<name>" answer the launch lines print — so the page and
// the startup log can never tell the user two different things.
func upstreamViews(routes []upstream.Route) []api.UpstreamView {
	table, err := upstream.New(routes)
	if err != nil {
		return nil
	}
	out := make([]api.UpstreamView, 0, len(routes))
	for _, r := range routes {
		suffix := ""
		if len(routes) > 1 {
			suffix = routeSuffix(table, r)
		}
		out = append(out, api.UpstreamView{Name: r.Name, URL: r.Base(), Path: suffix})
	}
	return out
}

// app is the wiring: store → recorder → proxy, plus the dashboard reading the
// same store. The E2E test builds one of these too, which is the point.
type app struct {
	handler  http.Handler
	store    *store.Store
	recorder *record.Recorder
	proxy    *proxy.Proxy

	stopSweep chan struct{} // closed to end the retention sweeper
	sweepDone chan struct{} // closed when it has ended
}

// sweepEvery is how often the capture retention window is enforced. Hourly:
// the windows on offer are a day and up, so nothing finer would delete anything
// sooner, and the setting is swept the moment it changes anyway.
const sweepEvery = time.Hour

// sweepLoop enforces retention — once at startup, so a window set last session
// applies before the dashboard is even open, then on the tick. It does nothing
// at all unless someone chose a window; the default is off.
func (a *app) sweepLoop(every time.Duration) {
	defer close(a.sweepDone)

	t := time.NewTicker(every)
	defer t.Stop()

	for {
		if n, err := api.Sweep(a.store, time.Now()); err != nil {
			log.Printf("tokentracer: capture sweep: %v", err)
		} else if n > 0 {
			log.Printf("tokentracer: capture sweep pruned %d captures", n)
		}
		select {
		case <-a.stopSweep:
			return
		case <-t.C:
		}
	}
}

func newApp(cfg config) (*app, error) {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}

	rec := record.New(st)

	table, err := upstream.New(cfg.Routes)
	if err != nil {
		st.Close()
		return nil, err
	}
	p, err := proxy.NewRouted(table, rec)
	if err != nil {
		st.Close()
		return nil, err
	}

	port, _ := strconv.Atoi(cfg.Port)
	dash := api.Handler(st, api.Config{Port: port, Upstream: cfg.Upstream(), Upstreams: upstreamViews(cfg.Routes)})

	// Everything that isn't ours belongs to the client's API call.
	mux := http.NewServeMux()
	mux.Handle("/", p)
	mux.Handle("/dashboard", dash)
	mux.Handle("/api/", dash)
	mux.Handle("/web/", dash)

	a := &app{
		handler:   mux,
		store:     st,
		recorder:  rec,
		proxy:     p,
		stopSweep: make(chan struct{}),
		sweepDone: make(chan struct{}),
	}
	go a.sweepLoop(sweepEvery)
	return a, nil
}

// close runs the shutdown order that guarantees nothing recorded is lost:
// the server stops first (so no new exchanges arrive), then the recorder drains
// what it already has, and only then does the database close under it.
func (a *app) close() {
	// The sweeper goes first and is waited for: it holds the database's single
	// connection while it vacuums, and closing the store out from under that
	// would turn a clean shutdown into a logged error for no reason.
	close(a.stopSweep)
	<-a.sweepDone

	// Shutdown ignores hijacked WebSockets. End them while the Recorder still
	// accepts the final partial Exchange from any response that was in flight.
	a.proxy.Close()
	a.recorder.Close()
	a.store.Close()
}

func main() {
	fmt.Fprint(os.Stderr, banner)

	// A saved .env counts as configured; the shell env outranks it. The wizard
	// runs on an explicit `setup`, or on first run — when nothing configured the
	// upstream and there is a terminal to ask on (pipes and CI get the default).
	shellUpstream := shellOnlyUpstream()
	fromShell := os.Getenv("UPSTREAMS") != "" || os.Getenv("UPSTREAM") != ""
	loadEnvFile(envFile)
	if shellUpstream {
		// A saved UPSTREAMS would otherwise outrank it, and the shell is the one
		// place the user is unambiguously overriding the saved answer right now.
		os.Unsetenv("UPSTREAMS")
	}
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		runSetup()
	} else if os.Getenv("UPSTREAMS") == "" && os.Getenv("UPSTREAM") == "" && stdinIsTTY() {
		runSetup()
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("tokentracer: %v", err)
	}

	a, err := newApp(cfg)
	if err != nil {
		log.Fatalf("tokentracer: %v", err)
	}

	addr := "127.0.0.1:" + cfg.Port
	srv := &http.Server{Addr: addr, Handler: a.handler}

	// Bind before printing the screen: "Ready" on a port something else owns
	// would be the screen lying about the one thing it exists to say.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("tokentracer: %v", err)
	}
	fmt.Fprint(os.Stderr, startupScreen(cfg, configSource(fromShell)))

	// Catch the signal before Serve, so a Ctrl-C during startup still takes the
	// clean path.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("tokentracer: %v", err)
		}
	}()

	<-ctx.Done()
	stop() // a second Ctrl-C now kills the process outright

	log.Printf("tokentracer: shutting down — flushing the queue (up to %s)", shutdownGrace)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("tokentracer: server shutdown: %v", err)
	}
	a.close()
	log.Print("tokentracer: done")
}
