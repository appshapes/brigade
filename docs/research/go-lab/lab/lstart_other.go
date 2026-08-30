//go:build unix && !linux

package main

func procStart(int) string { return "n/a (no procfs on this OS; ps -o lstart= is the source)" }
