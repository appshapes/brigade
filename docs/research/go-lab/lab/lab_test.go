package main

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

func TestReadNDJSONCap(t *testing.T) {
	var in bytes.Buffer
	in.WriteString("{\"a\":1}\n" + strings.Repeat("x", maxLine+1) + "\n{\"b\":2}\n")
	var got []string
	acc, drop, err := readNDJSON(&in, func(l []byte) { got = append(got, string(l)) })
	if err != nil || acc != 2 || drop != 1 {
		t.Fatalf("acc=%d drop=%d err=%v", acc, drop, err)
	}
}

func TestRaceDetectorLinks(t *testing.T) {
	var mu sync.Mutex
	n := 0
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); mu.Lock(); n++; mu.Unlock() }()
	}
	wg.Wait()
	if n != 4 {
		t.Fatal(n)
	}
}
