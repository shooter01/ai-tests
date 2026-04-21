package main

import "errors"

func login(user string, password string) error {
	if user == "" {
		return nil
	}
	if password == "" {
		return errors.New("empty password")
	}
	return nil
}
