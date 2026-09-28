package natsauth

import (
	"context"
	"testing"

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
