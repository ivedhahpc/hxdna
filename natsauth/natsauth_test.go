package natsauth

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

const (
	orgA    = "11111111-1111-1111-1111-111111111111"
	workerA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
)

// request builds an authorization request as a NATS server would send it, signed by server.
func request(t *testing.T, server nkeys.KeyPair, user, pass string) []byte {
	t.Helper()
	serverPub, _ := server.PublicKey()
	userKP, _ := nkeys.CreateUser()
	userPub, _ := userKP.PublicKey()
	rc := jwt.NewAuthorizationRequestClaims(userPub)
	rc.UserNkey = userPub
	rc.Server = jwt.ServerID{ID: serverPub}
	rc.ConnectOptions = jwt.ConnectOptions{Username: user, Password: pass}
	token, err := rc.Encode(server)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(token)
}

func TestRespond(t *testing.T) {
	issuer, _ := nkeys.CreateAccount()
	server, _ := nkeys.CreateServer()
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	workers := map[string]*Worker{
		workerA:                                {OrgID: orgA, KeyHash: HashKey(key), Active: true},
		"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb": {OrgID: orgA, KeyHash: HashKey(key), Active: false},
		"cccccccc-cccc-cccc-cccc-cccccccccccc": {OrgID: orgA, Active: true},
		// A lookup that matches loosely must still never widen permissions.
		"x.>":                                  {OrgID: orgA, KeyHash: HashKey(key), Active: true},
		"eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee": {OrgID: "*", KeyHash: HashKey(key), Active: true},
	}
	var results []error
	cfg := Config{
		Issuer:        issuer,
		SubjectPrefix: "hx",
		Lookup: func(_ context.Context, id string) (*Worker, error) {
			return workers[id], nil
		},
		OnResult: func(_ string, err error) { results = append(results, err) },
	}

	// An allowed worker gets a user JWT in the global account, fenced to its own subjects.
	resp, err := respond(cfg, request(t, server, workerA, key))
	if err != nil {
		t.Fatal(err)
	}
	rc, err := jwt.DecodeAuthorizationResponseClaims(string(resp))
	if err != nil {
		t.Fatal(err)
	}
	serverPub, _ := server.PublicKey()
	if rc.Error != "" || rc.Audience != serverPub {
		t.Fatalf("allowed: error %q audience %q", rc.Error, rc.Audience)
	}
	uc, err := jwt.DecodeUserClaims(rc.Jwt)
	if err != nil {
		t.Fatal(err)
	}
	if uc.Audience != GlobalAccount || uc.Name != workerA {
		t.Errorf("user claims: audience %q name %q", uc.Audience, uc.Name)
	}
	if got := uc.Sub.Allow; len(got) != 1 || got[0] != "hx.agents."+orgA+"."+workerA+".cmd.>" {
		t.Errorf("sub allow = %v", got)
	}
	if got := uc.Pub.Allow; len(got) != 1 || got[0] != "hx.agents."+orgA+"."+workerA+".>" {
		t.Errorf("pub allow = %v", got)
	}
	if uc.Resp == nil || uc.Resp.MaxMsgs != 1 || uc.Resp.Expires != ReplyWindow {
		t.Errorf("resp = %+v", uc.Resp)
	}
	if uc.Expires != 0 {
		t.Errorf("no SessionTTL: want a sign-in that never expires, got expires %d", uc.Expires)
	}

	// Every refusal gives the client the same reason and no JWT.
	for name, req := range map[string][]byte{
		"wrong key":      request(t, server, workerA, "not-the-key"),
		"inactive":       request(t, server, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", key),
		"no key issued":  request(t, server, "cccccccc-cccc-cccc-cccc-cccccccccccc", key),
		"unknown worker": request(t, server, "dddddddd-dddd-dddd-dddd-dddddddddddd", key),
		"no password":    request(t, server, workerA, ""),
		"wildcard ID":    request(t, server, "x.>", key),
		"wildcard org":   request(t, server, "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee", key),
	} {
		resp, err := respond(cfg, req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		rc, err := jwt.DecodeAuthorizationResponseClaims(string(resp))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if rc.Error != ErrNotAuthorized.Error() || rc.Jwt != "" {
			t.Errorf("%s: error %q jwt %q", name, rc.Error, rc.Jwt)
		}
	}
	if len(results) != 8 || results[0] != nil {
		t.Errorf("OnResult calls = %v", results)
	}

	// A request not signed by the NATS server it names gets no answer at all.
	other, _ := nkeys.CreateServer()
	forged := jwt.NewAuthorizationRequestClaims("UXXX")
	forged.Server = jwt.ServerID{ID: serverPub}
	token, _ := forged.Encode(other)
	if _, err := respond(cfg, []byte(token)); err == nil {
		t.Error("forged request: expected no answer")
	}
}

// With SessionTTL the sign-in expires, so the server drops the connection and the worker is
// checked again — a worker revoked while connected is cut off within the TTL.
func TestSessionTTL(t *testing.T) {
	issuer, _ := nkeys.CreateAccount()
	server, _ := nkeys.CreateServer()
	key, _ := NewKey()
	cfg := Config{
		Issuer:        issuer,
		SubjectPrefix: "hx",
		SessionTTL:    time.Hour,
		Lookup: func(_ context.Context, _ string) (*Worker, error) {
			return &Worker{OrgID: orgA, KeyHash: HashKey(key), Active: true}, nil
		},
	}
	before := time.Now()
	resp, err := respond(cfg, request(t, server, workerA, key))
	if err != nil {
		t.Fatal(err)
	}
	rc, err := jwt.DecodeAuthorizationResponseClaims(string(resp))
	if err != nil {
		t.Fatal(err)
	}
	uc, err := jwt.DecodeUserClaims(rc.Jwt)
	if err != nil {
		t.Fatal(err)
	}
	if lo, hi := before.Add(48*time.Minute).Unix(), time.Now().Add(time.Hour).Unix(); uc.Expires < lo || uc.Expires > hi {
		t.Errorf("expires %d, want between %d and %d (last fifth of the TTL)", uc.Expires, lo, hi)
	}

	// Lengths stay inside the last fifth of the TTL, and spread out rather than all landing on one.
	seen := map[time.Duration]bool{}
	for range 200 {
		d := sessionLength(time.Hour)
		if d < 48*time.Minute || d > time.Hour {
			t.Fatalf("session length %s outside [48m, 1h]", d)
		}
		seen[d.Truncate(time.Minute)] = true
	}
	if len(seen) < 5 {
		t.Errorf("session lengths barely spread: %d distinct minutes in 200", len(seen))
	}

	// Serve refuses a TTL that would churn every worker, or a negative one, before touching nc.
	for _, ttl := range []time.Duration{-time.Minute, time.Second, MinSessionTTL - time.Second} {
		bad := cfg
		bad.SessionTTL = ttl
		if _, err := Serve(nil, bad); err == nil {
			t.Errorf("SessionTTL %s: want an error", ttl)
		}
	}
}

func TestIssuerFromSeed(t *testing.T) {
	account, _ := nkeys.CreateAccount()
	seed, _ := account.Seed()
	if _, err := IssuerFromSeed(string(seed)); err != nil {
		t.Errorf("account seed: %v", err)
	}
	user, _ := nkeys.CreateUser()
	seed, _ = user.Seed()
	if _, err := IssuerFromSeed(string(seed)); err == nil {
		t.Error("user seed: expected an error")
	}
}
