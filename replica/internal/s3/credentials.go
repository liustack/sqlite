// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package s3

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// Credentials sign requests. Every fmt verb on them gives the access key ID
// and hides the rest. fmt calls no method on a field that is not exported,
// so a struct that keeps them in such a field prints them in full: keep
// them in a provider (Static), which holds them behind a pointer.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string // empty for long-term keys
}

// Format prints the credentials without the secret and the token.
func (c Credentials) Format(f fmt.State, verb rune) {
	fmt.Fprintf(f, "s3.Credentials{AccessKeyID: %q, SecretAccessKey: <hidden>}", c.AccessKeyID)
}

// A CredentialsProvider gives the credentials of each request, so that
// credentials that expire can be renewed. It is called for every request
// and caches what it fetches.
type CredentialsProvider interface {
	Credentials(ctx context.Context) (Credentials, error)
}

// Static gives fixed credentials. They are kept behind a pointer, so that a
// struct that holds the provider prints an address and not the secret,
// however deep fmt looks.
func Static(c Credentials) CredentialsProvider { return &static{c} }

type static struct{ c Credentials }

func (s *static) Credentials(context.Context) (Credentials, error) { return s.c, nil }

// Format prints the credentials without the secret and the token.
func (s *static) Format(f fmt.State, verb rune) { s.c.Format(f, verb) }

// EnvCredentials reads AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and
// AWS_SESSION_TOKEN at each request.
type EnvCredentials struct{}

// Credentials reads the variables.
func (EnvCredentials) Credentials(context.Context) (Credentials, error) {
	c := Credentials{
		AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return Credentials{}, errors.New("s3: AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are not both set")
	}
	return c, nil
}
