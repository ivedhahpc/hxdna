// Package natsauth is the control-plane half of locking a NATS server to enrolled workers.
// Workers never import it — it holds what only a control plane may do: issue a worker's NATS
// key, and answer the NATS server's auth callout, which asks on every connect who a client is
// and what it may use.
//
// A worker connects as user = its worker ID, password = the key issued at enrollment
// (hxdna.State.NATSOptions). The control plane stores only HashKey of that key. On connect the
// NATS server asks Serve's handler; the handler looks the worker up through the control
// plane's own Lookup, checks the key and that the worker is active, and answers with a user
// JWT allowing only that worker's own subjects (WorkerPermissions). Anything else is refused,
// so a revoked worker is refused on its next connect or reconnect.
//
// The control plane's own connections (command publisher, subscribers, this handler) bypass
// the callout as users listed in the NATS server's auth_callout auth_users. The server's
// auth_callout must not set xkey: encrypted requests aren't supported, and every connect
// would then be refused.
package natsauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ivedhahpc/hxdna"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

// CalloutSubject is where a NATS server with auth_callout sends its authorization requests.
const CalloutSubject = "$SYS.REQ.USER.AUTH"

// GlobalAccount is the account a config-mode NATS server without named accounts puts every
// user in, and the default Config.Account.
const GlobalAccount = "$G"

// ReplyWindow is how long after receiving a command a worker may still reply to it. The NATS
// server's own default is 2 minutes, shorter than a triage (240s) — a slower reply would be
// dropped silently.
const ReplyWindow = 10 * time.Minute

// lookupTimeout keeps a lookup inside the NATS server's own 2s callout timeout, so a slow
// database becomes a clean refusal rather than the server giving up mid-answer.
const lookupTimeout = 1500 * time.Millisecond

// maxInFlight caps requests answered at once. Every worker reconnects together after a NATS
// restart; answered one at a time, those at the back of the queue would pass the server's 2s
// timeout and be refused.
const maxInFlight = 32

// validToken reports whether s is one plain subject token. The worker ID comes from the
// connecting client and every ID ends up inside a permission subject, where ".", "*" or ">"
// would widen what it allows.
func validToken(s string) bool {
	return s != "" && !strings.ContainsAny(s, ". *>\t\r\n")
}

// ErrNotAuthorized is the only reason a refused client is given — never which check failed.
var ErrNotAuthorized = errors.New("not authorized")

// NewKey returns a new random worker NATS key, to hand to the worker once at enrollment. Store
// only HashKey of it.
func NewKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate NATS key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashKey is what the control plane stores for a worker's NATS key. A plain SHA-256 is enough:
// the key is 32 random bytes, so there is nothing to guess.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// WorkerPermissions is everything a worker may use on NATS: receive its own commands, publish
// under its own subjects (online, alert, result.*), and reply to a command it received within
// ReplyWindow. Nothing of another worker's or another org's. Every argument must be one plain
// subject token — the authorizer checks this before calling it.
func WorkerPermissions(prefix, orgID, workerID string) jwt.Permissions {
	var p jwt.Permissions
	p.Sub.Allow.Add(hxdna.WorkerSubject(prefix, orgID, workerID, "cmd.>"))
	p.Pub.Allow.Add(hxdna.WorkerSubject(prefix, orgID, workerID, ">"))
	p.Resp = &jwt.ResponsePermission{MaxMsgs: 1, Expires: ReplyWindow}
	return p
}

// Worker is what the control plane knows about a worker, for the authorizer to check.
type Worker struct {
	OrgID   string
	KeyHash string // HashKey of the key issued at enrollment; empty = none issued, never allowed
	Active  bool
}

// Lookup returns the control plane's record of a worker, or nil, nil when there is none. It
// must look the worker up by ID only — the authorizer does the checks.
type Lookup func(ctx context.Context, workerID string) (*Worker, error)

// Config configures Serve.
type Config struct {
	// Issuer signs the answers. Its public key is the auth_callout issuer in the NATS server
	// config — see IssuerFromSeed.
	Issuer nkeys.KeyPair
	// Account the worker is placed in. Empty = GlobalAccount.
	Account string
	// SubjectPrefix is the control plane's worker subject prefix (e.g. "hx").
	SubjectPrefix string
	Lookup        Lookup
	// OnResult, if set, is called once per request with the worker ID (as the client claimed
	// it) and nil or the real reason it was refused — for the control plane's own logging.
	OnResult func(workerID string, err error)
}

// IssuerFromSeed parses the issuer's account seed (an "SA…" nkey seed). Generate one with
// `nsc generate nkey --account` or nkeys.CreateAccount; its public key ("A…") goes in the NATS
// server config as auth_callout.issuer.
func IssuerFromSeed(seed string) (nkeys.KeyPair, error) {
	kp, err := nkeys.FromSeed([]byte(seed))
	if err != nil {
		return nil, fmt.Errorf("parse issuer seed: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil || !nkeys.IsValidPublicAccountKey(pub) {
		return nil, errors.New("issuer seed is not an account seed")
	}
	return kp, nil
}

// Serve answers the NATS server's auth callout on nc until the returned subscription is
// unsubscribed or nc closes. nc must be one of the auth_callout auth_users. Several control
// plane replicas may Serve at once; each request goes to one of them.
func Serve(nc *nats.Conn, cfg Config) (*nats.Subscription, error) {
	if cfg.Issuer == nil || cfg.Lookup == nil {
		return nil, errors.New("natsauth: Issuer and Lookup are required")
	}
	if !validToken(cfg.SubjectPrefix) {
		return nil, fmt.Errorf("natsauth: invalid SubjectPrefix %q", cfg.SubjectPrefix)
	}
	sem := make(chan struct{}, maxInFlight)
	return nc.QueueSubscribe(CalloutSubject, "hxdna-natsauth", func(msg *nats.Msg) {
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			if resp, err := respond(cfg, msg.Data); err == nil {
				_ = msg.Respond(resp)
			}
		}()
	})
}

// respond turns one authorization request into the signed answer. It errors only when there
// is no one to answer (a request that isn't from a NATS server); a refusal is still an answer.
func respond(cfg Config, data []byte) ([]byte, error) {
	req, err := jwt.DecodeAuthorizationRequestClaims(string(data))
	if err != nil {
		return nil, err
	}
	// Only a NATS server signs a request for itself; anything else publishing here is not one.
	if req.Issuer != req.Server.ID || !nkeys.IsValidPublicServerKey(req.Issuer) {
		return nil, errors.New("natsauth: request not signed by the NATS server")
	}

	workerID := req.ConnectOptions.Username
	userJWT, authErr := authorize(cfg, req)
	if cfg.OnResult != nil {
		cfg.OnResult(workerID, authErr)
	}

	rc := jwt.NewAuthorizationResponseClaims(req.UserNkey)
	rc.Audience = req.Server.ID
	if authErr != nil {
		rc.Error = ErrNotAuthorized.Error()
	} else {
		rc.Jwt = userJWT
	}
	token, err := rc.Encode(cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("natsauth: sign response: %w", err)
	}
	return []byte(token), nil
}

// authorize checks the client against the control plane's record and, when allowed, returns
// the signed user JWT carrying its permissions.
func authorize(cfg Config, req *jwt.AuthorizationRequestClaims) (string, error) {
	workerID, key := req.ConnectOptions.Username, req.ConnectOptions.Password
	if workerID == "" || key == "" {
		return "", errors.New("no worker ID or key presented")
	}
	if !validToken(workerID) {
		return "", errors.New("worker ID is not a plain subject token")
	}
	if !validToken(cfg.SubjectPrefix) {
		return "", errors.New("invalid subject prefix")
	}

	ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
	defer cancel()
	w, err := cfg.Lookup(ctx, workerID)
	if err != nil {
		return "", fmt.Errorf("lookup: %w", err)
	}
	if w == nil {
		return "", errors.New("unknown worker")
	}
	if w.KeyHash == "" || subtle.ConstantTimeCompare([]byte(HashKey(key)), []byte(w.KeyHash)) != 1 {
		return "", errors.New("wrong key")
	}
	if !w.Active {
		return "", errors.New("worker is not active")
	}
	if !validToken(w.OrgID) {
		return "", errors.New("worker's org ID is not a plain subject token")
	}

	uc := jwt.NewUserClaims(req.UserNkey)
	uc.Name = workerID
	uc.Audience = cfg.Account
	if uc.Audience == "" {
		uc.Audience = GlobalAccount
	}
	uc.Permissions = WorkerPermissions(cfg.SubjectPrefix, w.OrgID, workerID)
	vr := jwt.CreateValidationResults()
	uc.Validate(vr)
	if errs := vr.Errors(); len(errs) > 0 {
		return "", fmt.Errorf("invalid user claims: %w", errs[0])
	}
	return uc.Encode(cfg.Issuer)
}
