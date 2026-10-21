//go:build !linux

package main

func databaseOwnerCommand(string) (bool, error) { return false, nil }

func privateDatabaseUmask() {}

func lockDatabaseAdministration(string) (func(), error) { return func() {}, nil }
