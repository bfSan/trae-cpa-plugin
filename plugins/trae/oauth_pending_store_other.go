//go:build !unix

package main

import "time"

// Explicitly memory-only on non-Unix until an equivalent secure file/locking
// implementation exists. No HOME or configured-auth-dir writes; persistent=false.
func openOAuthPendingStore(string) (*oauthPendingStore, error)      { return nil, nil }
func (*oauthPendingStore) read(string) (*pendingLoginRecord, error) { return nil, errOAuthStorage }
func (*oauthPendingStore) write(*pendingLoginRecord) error          { return errOAuthStorage }
func (*oauthPendingStore) scan(time.Time) (int, error)              { return 0, errOAuthStorage }
func (*oauthPendingStore) remove(string)                            {}
func (*oauthPendingStore) close()                                   {}

func (*oauthPendingStore) records() ([]*pendingLoginRecord, error) { return nil, errOAuthStorage }

func readSafeHostCallback(string, string, string) (string, string, bool) { return "", "", false }
