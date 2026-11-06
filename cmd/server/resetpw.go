package main

import (
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"iptv-proxy/internal/auth"
	"iptv-proxy/internal/store"
)

// resetPasswordCmd is `iptv-proxy reset-password [-data DIR] [USER]`: the way back in when the superuser has lost the password.
// It needs access to the data directory, which already means full control of the installation, so it asks nothing more. It prints a
// new random password once. Run it next to the running app (docker compose exec iptv-proxy iptv-proxy reset-password).
func resetPasswordCmd(args []string) error {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	data := fs.String("data", "/data", "directory with the database")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: iptv-proxy reset-password [-data /data] [user name]\nSets a new random password for the user (default: the superuser) and prints it.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	u, pw, err := resetPassword(*data, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Printf("New password for %s (%s): %s\nThey are signed out everywhere. Sign in and change it (click your name in the header).\n", u.Username, u.Role, pw)
	return nil
}

// resetPassword gives a user (the superuser when name is empty) a new random password and returns it.
func resetPassword(dataDir, name string) (*store.User, string, error) {
	if _, err := os.Stat(filepath.Join(dataDir, "iptv.db")); err != nil {
		return nil, "", fmt.Errorf("no database in %s (use -data to point at the data directory)", dataDir)
	}
	st, err := store.Open(dataDir)
	if err != nil {
		return nil, "", err
	}
	defer st.Close()

	var u *store.User
	if name != "" {
		if u, err = st.UserByName(name); err != nil {
			return nil, "", fmt.Errorf("there is no user %q", name)
		}
	} else {
		users, err := st.ListUsers()
		if err != nil {
			return nil, "", err
		}
		for i := range users {
			if users[i].Role == auth.Superuser {
				u = &users[i]
			}
		}
		if u == nil {
			return nil, "", errors.New("there is no superuser yet: open the app and create it on the setup page")
		}
	}
	pw, err := randomPassword(16)
	if err != nil {
		return nil, "", err
	}
	// a new hash changes the session fingerprint, which signs the user out everywhere
	if err := st.UpdateUser(u.ID, "", auth.Hash(pw), ""); err != nil {
		return nil, "", err
	}
	return u, pw, nil
}

// randomPassword uses letters and digits that are hard to mix up when typed from a screen.
func randomPassword(n int) (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b), nil
}
