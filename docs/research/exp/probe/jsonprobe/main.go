package main

import (
	"encoding/json"
	"fmt"
)

func main() {
	var v map[string]any
	fmt.Println("dup keys:", json.Unmarshal([]byte(`{"a":1,"a":2}`), &v), v)
	fmt.Println("bad utf8:", json.Unmarshal([]byte("{\"a\":\"\xff\"}"), &v))
	var s struct{ A int }
	fmt.Println("unknown field (loose by default):", json.Unmarshal([]byte(`{"A":1,"zzz":2}`), &s), s)
}
