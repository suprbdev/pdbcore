package jwtmint

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/suprbdev/pdbcore/introspect"
)

var opts = Options{Schema: "public", Type: "jwt", Secret: "s3cret", Issuer: "pdb", Audience: "api"}

func parse(t *testing.T, tok string) jwt.MapClaims {
	t.Helper()
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(tok, claims, func(*jwt.Token) (any, error) { return []byte(opts.Secret), nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("pdb"), jwt.WithAudience("api"))
	if err != nil {
		t.Fatalf("minted token does not verify: %v", err)
	}
	return claims
}

func TestEnabled(t *testing.T) {
	if (Options{}).Enabled() {
		t.Fatal("empty options must be disabled")
	}
	if !opts.Enabled() {
		t.Fatal("configured options must be enabled")
	}
}

func TestMintsFunction(t *testing.T) {
	yes := &introspect.Function{ReturnTypeSchema: "public", ReturnType: "jwt"}
	set := &introspect.Function{ReturnTypeSchema: "public", ReturnType: "jwt", ReturnsSet: true}
	no := &introspect.Function{ReturnTypeSchema: "public", ReturnType: "users"}
	other := &introspect.Function{ReturnTypeSchema: "auth", ReturnType: "jwt"}
	if !opts.MintsFunction(yes) || !opts.MintsFunction(set) {
		t.Fatal("matching return type must mint")
	}
	if opts.MintsFunction(no) || opts.MintsFunction(other) || opts.MintsFunction(nil) {
		t.Fatal("non-matching return type must not mint")
	}
	if (Options{}).MintsFunction(yes) {
		t.Fatal("disabled options must not mint")
	}
}

func TestMintSingle(t *testing.T) {
	exp := time.Now().Add(time.Hour).Unix()
	raw := json.RawMessage(`{"exp":` + jsonInt(exp) + `,"user_id":7,"role":"app_user","nothing":null}`)
	out, err := Mint(opts, raw, false)
	if err != nil {
		t.Fatal(err)
	}
	var tok string
	if err := json.Unmarshal(out, &tok); err != nil {
		t.Fatalf("minted value must be a JSON string: %s", out)
	}
	claims := parse(t, tok)
	if claims["role"] != "app_user" || claims["user_id"] != float64(7) {
		t.Fatalf("claims = %v", claims)
	}
	if _, ok := claims["nothing"]; ok {
		t.Fatal("nil claims must be dropped")
	}
	if claims["iss"] != "pdb" || claims["aud"] != "api" {
		t.Fatalf("iss/aud missing: %v", claims)
	}
}

func TestMintNullAndSet(t *testing.T) {
	out, err := Mint(opts, json.RawMessage("null"), false)
	if err != nil || string(out) != "null" {
		t.Fatalf("null = %s, %v", out, err)
	}
	out, err = Mint(opts, json.RawMessage(`[{"role":"a","exp":`+jsonInt(time.Now().Add(time.Hour).Unix())+`},null]`), true)
	if err != nil {
		t.Fatal(err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(out, &items); err != nil || len(items) != 2 {
		t.Fatalf("set = %s", out)
	}
	var tok string
	if err := json.Unmarshal(items[0], &tok); err != nil {
		t.Fatalf("first item must be a token: %s", items[0])
	}
	if parse(t, tok)["role"] != "a" {
		t.Fatal("wrong claims in set item")
	}
	if string(items[1]) != "null" {
		t.Fatalf("null set item = %s", items[1])
	}
}

func TestMintErrors(t *testing.T) {
	if _, err := Mint(opts, json.RawMessage(`"not an object"`), false); err == nil {
		t.Fatal("expected claims error")
	}
	if _, err := Mint(opts, json.RawMessage(`{}`), true); err == nil {
		t.Fatal("expected set error")
	}
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
