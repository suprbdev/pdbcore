// Package jwtmint turns function results of one composite type into signed
// JWTs (PostGraphile's pgJwtType): a function returning schema.type yields
// an HS256 token string whose claims are the composite's fields; an exp
// field becomes the token expiry.
package jwtmint

import (
	"encoding/json"
	"fmt"

	"github.com/golang-jwt/jwt/v5"

	"github.com/suprbdev/pdbcore/introspect"
)

// Options selects the composite type and the signing parameters.
type Options struct {
	Schema   string // pg schema of the composite, e.g. "public"
	Type     string // composite type name, e.g. "jwt"
	Secret   string
	Issuer   string
	Audience string
}

// Enabled reports whether minting is configured.
func (o Options) Enabled() bool { return o.Type != "" }

// MintsFunction reports whether f's return type is the mint composite (or
// SETOF it).
func (o Options) MintsFunction(f *introspect.Function) bool {
	return o.Enabled() && f != nil && f.ReturnTypeSchema == o.Schema && f.ReturnType == o.Type
}

// Mint signs one claims object (or, with set, each element of an array of
// them) rendered as JSON, returning the JSON string(s). JSON null stays
// null.
func Mint(o Options, raw json.RawMessage, set bool) (json.RawMessage, error) {
	if set {
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return raw, fmt.Errorf("mint: set: %w", err)
		}
		for i, item := range items {
			minted, err := Mint(o, item, false)
			if err != nil {
				return raw, err
			}
			items[i] = minted
		}
		return json.Marshal(items)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return raw, fmt.Errorf("mint: claims: %w", err)
	}
	if claims == nil {
		return json.RawMessage("null"), nil
	}
	token, err := Sign(o, claims)
	if err != nil {
		return raw, fmt.Errorf("mint: sign: %w", err)
	}
	return json.Marshal(token)
}

// Sign builds the HS256 token for one claims map: nil-valued claims are
// dropped and Issuer/Audience are added when set.
func Sign(o Options, claims map[string]any) (string, error) {
	mc := jwt.MapClaims{}
	for k, v := range claims {
		if v != nil {
			mc[k] = v
		}
	}
	if o.Issuer != "" {
		mc["iss"] = o.Issuer
	}
	if o.Audience != "" {
		mc["aud"] = o.Audience
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, mc).SignedString([]byte(o.Secret))
}
